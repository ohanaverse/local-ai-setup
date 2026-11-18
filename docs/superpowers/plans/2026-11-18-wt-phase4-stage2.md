# Phase 4 Stage 2 (the hard cut) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every host-admin command and the registry/setup editors out of `wt-session` into `wt-model` (old names become exit-1 pointers), so a `wt-session` binary links no registry, provider-lifecycle, LiteLLM, launchd or cloud-sync code and still configures `native` and `remote` mode on its own.

**Architecture:** Shared code is cut by who runs it: new session-safe leaves `urlrule` (the one url `@` rule), `modelping` (one ping and its classification), `modelmode` (native/remote State, Observe, Plan, Apply) and `editorkit`/`modeview` (UI plumbing), which the admin package `modelsetup` extends by embedding and delegating. `configeditor` splits into `sessioneditor` (`wt config`: Agents + Mode) and `modeleditor` (`wt-model model`/`wt-model setup`: Models + Setup), and the command families move to `cmd/wt-model` one PR at a time with their callers; the depcheck ratchet ends holding `cmd/wt-session` off every admin package.

**Tech Stack:** Go (wt/ module), cobra, bubbletea, Python (llmbench, modeltiers), bash.

**Spec:** docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md

The binding interface spine the stage sections were planned against is reproduced as **Appendix A** at the end of this file; "spine §N" anywhere below means Appendix A §N.

## Global Constraints

- **Ratchet lists (`wt/internal/depcheck/depcheck_test.go`), pinned in the PR that creates each package:** `sessionLibraries` gains `internal/urlrule`, `internal/modelping` (1a), `internal/modelmode` (1b), `internal/editorkit` (2), `internal/modeview` (3a), `internal/sessioneditor` (3c), `internal/cli` (4a-i); `sessionBanned` gains `internal/sessioneditor` (3c); `adminBanned` gains `internal/modeleditor` (3b) and `internal/servestate` (4d-i); `internal/configeditor` leaves `adminBanned`, `sessionBanned` and `adminScreens` when deleted (3c); `adminScreens` = the editors plus `internal/editorkit`, all off `internal/tui`; `modeLeaves` (`TestModeLeavesStayOffTheClient`) = `urlrule`, `modelping`, `modelmode`, `editorkit`, `modeview`, `cli` — none may link `internal/modelclient`; `sessionBinaryAllow = []string{"internal/lifecycle", "internal/litellm", "internal/localmodels", "internal/spend"}` (4d-ii), emptied in 5a; every allow-list only shrinks.
- **Session binary off the admin packages:** after 5a `cmd/wt-session`'s non-test closure contains none of `modeladmin`, `modeleditor`, `modelsetup`, `cloudsync`, `hoststop`, `localmodels`, `litellm`, `lifecycle`, `spend`, `candidates`, `servestate` or the server packages (`TestSessionBinaryStaysOffTheAdminPackages`); it still links the leaf libraries `config` and `provider`. `cmd/wt-model` never links anything in `sessionBanned` (incl. `modelclient`, `sessioneditor`).
- **The url `@` rule has exactly one definition:** `urlrule.HasUserinfo` / `urlrule.Hidden` (`"(hidden: the url contains an @)"`) / `urlrule.Shown`; `modelclient` and `config/proxyurl.go` only delegate; `TestProxyURLRuleIsModelclientsRule` is deleted (it would be an import cycle).
- **No token in the serve record:** `<wt-model home>/serve.json` holds only `version` (1), `pid`, `listen`, `token_file` (an absolute path), `started`; file 0600 in a 0700 directory; written before the listening line; removed on clean exit only when it names this pid; a stale record is tolerated because every reader pings first; tests never write the real config home.
- **Pointers exit 1:** every moved name prints exactly one line `wt: <old> moved to wt-model — run 'wt-model <new>'; nothing was changed` (`<old>` without the `wt ` prefix) and exits 1, changing no file; stubs are hidden, `DisableFlagParsing`, `SilenceUsage`, annotated `removedCmdAnnotation`; one table test (`TestMovedCommandsPointAtWtModel`) covers every row and conditional pointer; a pointer never exits 0 and an old name never still works.
- **Callers move in the same PR as their command** (modeltiers, llmbench, `bin/update/*`, `bin/mlx-quantize`, `benchmarks/mtplx-profile-matrix`, skills, Go strings, docs, fixtures' readers). `wt model list --server`, `wt stop <model>`, `wt litellm status|set` stay in `wt-session`.
- **Quoting an old name:** text that is kept after its PR (docs, skills, comments, help, scripts; not `wt/CHANGELOG.md`, which every gate excludes, nor `moved.go`/`moved_test.go`) never writes `wt` and the moved command as adjacent words (`wt cloud-sync`, `wt litellm restart`, `wt served`, `wt model add`, `wt stop --all`). To say that the old name points elsewhere, write the subcommand alone and name the binary apart: "`cloud-sync` run under `wt` (its name before phase 4 stage 2) prints `wt: cloud-sync moved to wt-model — …` and exits 1". The pointer line itself (`wt: <old> moved …`) never matches, because of the colon. Every old-name gate (4a-ii, 4a-iii, 4b, 4c, 4d-ii, Task 75) relies on this; none has a quotation exclusion.
- **Every new `Test*` has a top-level `//` comment** saying what it pins and why; a moved test keeps its name and comment.
- **No secrets and no real home paths** in code, tests, goldens or docs: write `/Users/user_name`; guides embed no live model state.
- **PR bodies say `Refs #N`**, never close/fix/resolve followed by `#N`; write them with `gh pr create --body-file`; end with the attribution line.
- **Commands per package:** Go from `wt/`: `go build ./... && go vet ./... && make check && go test ./... -count=1` plus `go test ./internal/depcheck ./internal/removal -count=1` named; modeltiers `cd modeltiers && make check && make test`; llmbench `cd llmbench && make check && make test`; repo root `make lint` (`lint-shell`, `lint-skills`, `check-links`) and `make test-all` (5b).
- **`cmd/wt-session` tests take ~3.5 min:** inside a task run `go test ./cmd/wt-session -run '<subset>'`; the whole package runs once per PR gate, in the background, with the exit captured in the log (`echo "GATE_EXIT=$?"`).
- **The tree is green after every task** (build, vet, gofmt, the task's tests); a task that cannot leave it green is one commit (Task 24 is planned atomic for that reason).
- **Admin goldens never change by accident:** never run `UPDATE_GOLDEN=1` in `internal/modelsetup`; edit a golden line by hand only where a task says so (Task 52).
- **Scratch runs:** any ad-hoc `wt`/`wt-model` against a scratch registry also sets `WT_LITELLM_CONFIG` (and `WT_LITELLM_RESTART_CMD`); `modeltiers run` against one sets `MODELTIERS_OUT` and `MODELTIERS_CACHE`.

## Review Focus

The five input classes the design implies that are most likely to bite a person using the software, most likely first, each pinned to the task that owns the code:

1. **A script or habit still calling an old name** (cron jobs, shell aliases, an old `bin/update/litellm`, an unmigrated checkout of llmbench). It must get the one pointer line and exit 1, never cobra's parent help with exit 0, and the stub must swallow the old command's own flags. Pinned by `TestMovedCommandsPointAtWtModel` (Task 36, rows added in Tasks 37, 38, 42, 45, 49–51, 60), whose rows append `--json x` and, for cloud-sync, the real flag set (Task 45 Step 1.6); `TestAddMovedStubsFindsOrAddsTheParent`'s panic (Task 36) so a stub can never shadow a live command; `TestWtLitellmHelpListsOnlyTheProxyPairCommands` (Task 51) so an old updater's `--help | grep restart` finds nothing; `test_warm_and_served_need_wt_model_on_path` (Task 43); the repo-wide sweep (Task 75).
2. **A user with only `wt-session` installed who chooses `local`** (the Mode tab allows it everywhere). The plan must store the defaults (keeping a stored url and token file only when the stored mode is already `local`, so a remote url never becomes local's), show the pointer note, never offer a `wt-model setup` that is not on PATH, and the next launch must name `wt-model setup` in its unreachable hint. Pinned by `TestModeTabWithoutWtModelOnPathOnlyPoints`, `TestModeTabRunSetupIsDefaultNo`, `TestModeTabLocalPlansThePointer`, `TestModeTabRemoteToLocalNeverKeepsTheRemoteURL` (Task 30); `TestGateGoesOnWhenWtModelSetupFails` and the `models_test.go:767` hint change (Task 31); `plan_local_pointer.golden` and `TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal` (Task 10).
3. **The `Observe` fallback refactor silently changing an old behaviour** (the agent's token file as the fallback, the agent's listen address as the local url default, `TokenFiles` order, the ping now running before the listener/LiteLLM probes). Pinned by the baseline `comm` check run after each of Tasks 7–12 (only `TestObserveClassifiesEachPingFailure` may disappear); `TestObserveUsesTheLocalFallbackOnlyInLocalMode`, `TestObserveWithoutAFallbackPingsTheDefaultLocalURL`, `TestObservePassesTheAgentAsTheLocalFallback` (Task 8); the unchanged admin goldens (`git diff --stat internal/modelsetup/testdata` empty, Task 10).
4. **A hand-written `[model-server] mode = "local"` with no url** (or with a url but no token file). The session tab pings `http://127.0.0.1:8787` (not the agent's listen address, which it cannot read), and the next Mode-tab save keeps what is stored (the stored mode is already `local`) and writes the defaults only into the empty fields. Pinned by `TestObserveWithoutAFallbackPingsTheDefaultLocalURL` (Task 8) and `TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal` / `TestBuildPlanLocalPointerNeedsAConfigHomeForTheDefaultToken` (Task 10), whose hand-written case is `Table{Mode: "local", TokenFile: "/x/t"}` with no url, planning `p.Want.URL == "http://" + DefaultListen` and `p.Want.TokenFile == "/x/t"` (only the empty field is filled).
5. **A stale serve runtime record** (wt-model killed, rebooted, or a second serve that overwrote the record). Host stops must print the one note and carry on exactly as today, never hang, never delete another server's record, never print the token. Pinned by `TestSessionsReportsAStaleRecord` and `TestSessionsGivesUpOnAServerThatNeverAnswers` (Task 56), `TestServeKeepsARecordAnotherServerWrote` and `TestServeThatCannotListenLeavesTheRecordAlone` (Task 57), `TestHostSessionReaderNotesAnUnreachableServer` / `…NeverPrintsTheToken` (Task 58), `TestStopNotesAnUnreadableServerAndGoesOn` (Task 59).

Also covered and worth a reviewer's glance: `wt config` on a machine whose `config.toml` does not parse still opens with writes disabled (`TestWtConfigOpensWhenConfigTomlIsSyntaxBroken`, Task 31; `TestModeTabDisablesWritesOnAParseError`, Task 30), and `wt config` on a machine whose `registry.toml` is garbage opens without reading it (`TestWtConfigReadsNoRegistry`, Task 31).

---

## Execution notes

**PR order** (each PR branches off `origin/main` after the previous one is squash-merged; entry = the previous PR merged; every exit gate includes `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` green and `go test ./internal/depcheck ./internal/removal -count=1` named in the PR body):

| PR | Tasks | Scope | Extra exit gate |
|---|---|---|---|
| 1a | 1–3 | `urlrule`, `modelping`; modelclient delegates | G-fast, G-session; `make check-links` |
| 1b | 4–6 | `modelmode` rules, words, steps, seams; modelsetup stops importing modelclient | `go list -f '{{join .Imports "\n"}}' ./internal/modelsetup \| grep -c internal/modelclient` = 0 |
| 1c | 7–12 | `modelmode` State/Observe/Plan/Apply; goldens split | baseline `comm` check; `go list -deps ./internal/modelmode \| grep -c internal/modelclient` = 0 |
| 2 | 13–18 | `editorkit` | `go test ./cmd/wt-session -run 'TestConfig\|TestGate\|TestWtConfigOpens'` |
| 3a | 19–22 | sync helpers into `litellm`, `ValidateSession`, `modeview` | `go test ./internal/litellm ./internal/modeview ./internal/config ./internal/configeditor` |
| 3b | 23–26 | `modeleditor` (Models), `wt-model model`, bare `wt model` pointer | `go test ./internal/modeleditor ./cmd/wt-model` |
| 3c | 27–32 | Setup tab, `wt-model setup`, `sessioneditor`, `wt config` switched, `configeditor` deleted | `go list -deps ./cmd/wt-session \| grep -E 'internal/(configeditor\|modeleditor\|modelsetup)$'` empty; `make single-p` |
| 4a-i | 33–36 | `internal/cli`, pointer stub family | `go list -deps ./internal/cli` names only `cli`, `exitcode`; `make lint-skills && bin/check-links` |
| 4a-ii | 37–41 | `wt-model model init\|list\|add\|edit\|rm` + modeltiers, scripts, skills | `cd modeltiers && make check && make test`; old-name grep |
| 4a-iii | 42–44 | `wt-model served`, `warm` + llmbench | `cd llmbench && make check && make test`; old-name grep |
| 4b | 45–48 | `wt-model cloud-sync` | root `make lint`; `wt cloud-sync` grep empty; built-binary checks of Task 45 Step 4 |
| 4c | 49–53 | `wt-model litellm sync\|list\|providers\|restart`, `bin/update/litellm` | `make lint-shell && make check-links`; old-name grep empty |
| 4d-i | 54–57 | `config.ModelHome`, `servestate`, serve writes/removes `serve.json` | `go test ./internal/servestate ./internal/config ./cmd/wt-model` |
| 4d-ii | 58–63 | `wt-model stop <provider>\|--all`, `wt stop` pointer, binary ratchet | `go test ./internal/hoststop ./internal/lifecycle`; Task 62 grep empty |
| 5a | 64–69 | stats/answerer cut loose, ratchet emptied, boundary scan, floors | `make single-p`; `../bin/check-config-dirs-untouched` |
| 5b | 70–76 | install/update, docs sweep, command homes, As built | root `make lint`, `make test-all`, `test_guide_samples.py`; Task 75 sweep empty |

**Subagent-driven development guidance:**
- **The riskiest PR is 1 (1a–1c, above all 1c's `Observe` cut, Task 8).** Use the most capable model for its implementer and its reviewer; record the baseline test list before Task 7 and run the `comm` check after every task of 1c. 3c (Tasks 27–32) is the second riskiest (a whole-package deletion and the first-run gate); give it the same treatment.
- Run the full `cmd/wt-session` package once per PR gate, in the background with the exit captured (`( … ; echo "GATE_EXIT=$?" ) > log`, no inner `&`); inside tasks use the `-run` subsets each task names.
- **One worktree per PR, stacked on main after each merge.** The rebase lesson: PRs are squash-merged, so a branch cut from an unmerged predecessor carries commits main never sees; always branch each PR off `origin/main` after the previous merges (`git fetch && git switch -c <branch> origin/main`), never off the previous PR's branch.
- The SDD workspace lives in the worktree: copy the ledger forward when the next PR's worktree is created.
- Line numbers in the tasks are `origin/main` f60ad48a; earlier PRs shift them, so locate each block by the function or test name quoted beside it.
- 1c and 3c exceed the ~1,500-line bar and cannot be cut without a broken intermediate; review them commit by commit as their sections say.

**Assembler reconciliation (applied to the stage sections below):** stage-local task ids (S1.1 … S5.13) are renumbered to Tasks 1–76 and every cross-reference follows; PR 3b's `cmd/wt-model/host.go` uses the field and method names the later PRs consume (`hostCfg`, `hostLoadErr`, `hostCfgErr`, `hostSet`, `hostConfig()`, `hostConfigErr()`, as in `cmd/wt-session/app.go`), one `h := &host{}` built in `rootCmd`, and `fakeDeps` (the existing helper name); Task 36 extends the `moved.go`/`moved_test.go` Task 23 created instead of creating them; Task 24 appends `internal/modeleditor` to the `adminScreens` list Task 13 introduced; the stage planners' "open question" references are resolved inline.

---

## PR 1: urlrule, modelping and modelmode extraction (no behaviour change)

**Entry:** `origin/main` at f60ad48a (or later with no change under `wt/internal/{modelclient,modelsetup,configeditor,config,depcheck}`), tree green (`cd wt && make check && go test ./... -count=1`).
**Exit gate (each of 1a, 1b, 1c):** `cd wt && make check && go build ./... && go vet ./... && go test ./... -count=1` green (run in the background, about 4 min; read the log for the exit), then `go test ./internal/depcheck ./internal/removal -count=1 -v | grep -E '^(--- FAIL|ok|FAIL)'` shows two `ok` lines, and `make check-links` from the repo root (each PR edits `wt/CLAUDE.md` and `wt/docs/internals/packages.md`).
**Shape:** the spine ships this stage as three PRs, **1a** (Task 1 to 3), **1b** (Task 4 to 6) and **1c** (Task 7 to 12), each one gated as above; every task below leaves the whole tree green.

Paths are under `wt/`. Module path prefix `github.com/ohanaverse/local-ai-setup/wt/internal/`. "from X:N" is the file and line on f60ad48a. Every new `Test*` gets a top-level `//` comment that says what it pins and why. No binary changes behaviour in this PR: every output string, exit code and plan text stays byte-identical; the only new behaviour is `modelmode`'s local pointer plan and its nil-hook note (Task 10), which no binary reaches yet.

### The safety net, and what runs after each task

The existing observe/plan/apply suites of `internal/modelsetup` (about 180 tests), the Model server tab tests of `internal/configeditor` (they drive the real `modelsetup.Observe`/`BuildPlan` through `server_aturl_test.go`) and the client tests of `internal/modelclient` are the net. Rules:

- No existing assertion is edited except to spell a moved field (`st.Host.Binary` → `st.Tools.Binary`, `p.Want.MasterKey` → `p.Admin.MasterKey`) or an embedded composite literal. The admin goldens (`internal/modelsetup/testdata/*.golden`) never change: never run with `UPDATE_GOLDEN=1` in `internal/modelsetup`.
- A test that "moves" is deleted from its old file and added to the new package in the same task; the test name is kept unless the task says otherwise.
- Before Task 7, record the names of the passing tests: `cd wt && go test ./internal/modelsetup ./internal/configeditor -count=1 -v 2>&1 | grep -E '^--- PASS: Test' | awk '{print $3}' | sort -u > "$TMPDIR/s1-baseline.txt"`. After each of Task 7 to 12 run `go test ./internal/modelsetup ./internal/modelmode ./internal/configeditor -count=1 -v 2>&1 | grep -E '^--- PASS: Test' | awk '{print $3}' | sort -u > "$TMPDIR/s1-now.txt" && comm -23 "$TMPDIR/s1-baseline.txt" "$TMPDIR/s1-now.txt"`: the output lists only the tests the task deleted on purpose (Task 8: `TestObserveClassifiesEachPingFailure`, which `modelping`'s `TestClassifyMapsEachPingError` replaces; nothing else in any task).

Named checks used by the steps (run from `wt/`):

- **G-fast**: `test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go test ./internal/urlrule ./internal/modelping ./internal/modelmode ./internal/modelclient ./internal/modelsetup ./internal/configeditor ./internal/config ./internal/depcheck ./internal/removal -count=1` (leave out a package path the tree does not have yet).
- **G-session**: `go test ./cmd/wt-session -run 'Gate|Native|Remote|Unreachable|AtURL|PlainURLs|OpenModels|HalfConfigured|ModelClient|ExplicitNative|CollectUsage' -count=1 && go test ./internal/modelserver -run 'TestCleanAgreesWithCleanupFlatten|TestClientAgainstTheRealServer' -count=1`.

---

### PR 1a: `urlrule` and `modelping`

### Task 1: `internal/urlrule`, the one url `@` rule

**Files:**
- Create: `internal/urlrule/urlrule.go`, `internal/urlrule/urlrule_test.go`
- Modify: `internal/modelclient/urlcreds.go:15-24` (HiddenURL, HasUserinfo become delegations; the rule's comment block :5-13 moves to `urlrule.go`), `internal/config/proxyurl.go:8-37`, `internal/config/proxyurl_test.go:11,27-58`, `internal/depcheck/depcheck_test.go:43-49` (+ new test at the end), `CLAUDE.md` (wt's: package list line 94, ratchet paragraph line 307), `docs/internals/packages.md` (new row after the `modelclient/mode.go` row :76; rule bullets :91-93)
- Test: `internal/urlrule/urlrule_test.go`, `internal/config/proxyurl_test.go`, `internal/depcheck/depcheck_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (stdlib only):
  ```go
  package urlrule
  const Hidden = "(hidden: the url contains an @)"
  func HasUserinfo(raw string) bool   // strings.Contains(raw, "@")
  func Shown(raw string) string       // Hidden when HasUserinfo(raw), else raw
  ```
  `modelclient`: `const HiddenURL = urlrule.Hidden`; `func HasUserinfo(raw string) bool { return urlrule.HasUserinfo(raw) }`.
  `config/proxyurl.go` keeps `ErrProxyURLHasAt` (:24) and becomes three delegations: `const ProxyURLHidden = urlrule.Hidden`, `func ProxyURLHasAt(raw string) bool { return urlrule.HasUserinfo(raw) }`, `func ShownProxyURL(raw string) string { return urlrule.Shown(raw) }`. Callers (`cmd/wt-model/serve.go`, `cmd/wt-session/litellm.go`, `config/proxyhome.go`, `lifecycle/profile_routes.go`, `litellm/restart.go`) are not touched.
  depcheck: `var modeLeaves = []string{"internal/urlrule"}` and `TestModeLeavesStayOffTheClient`.

- [ ] **Step 1: Write the failing tests.**
  - `internal/urlrule/urlrule_test.go`:
    - `TestHasUserinfoIsAnyASCIIAt`: true for each of `"USERZ:PASSZ@litellm:4000"`, `"http://USERZ:PASSZ@localhost:4000"`, `"//USERZ:PASSZ@hq"`, `"USERZ:p/PASSZ@hq"`, `"http://localhost:4000/?k=USERZ@PASSZ"`, `"@"`, `Hidden`; false for `"http://localhost:4000"`, `"https://proxy.example:4443/v1"`, `"http://h/u＠x"` (U+FF20), `"http://h/u%40x"`, `""`.
    - `TestShownReplacesAnAtURLWhole`: `Shown(x) == Hidden` for each true case above, `Shown(x) == x` for each false case; `strings.Contains(Hidden, "@")` (the placeholder is refused like the url it stands for).
  - `internal/config/proxyurl_test.go`: delete `TestProxyURLRuleIsModelclientsRule` (:37-58) and the `modelclient` import (:11) — required: from Task 4 `modelclient` imports `modelmode`, which imports `config`, so this internal test would be an import cycle. Add `TestShownProxyURLHidesOnlyAnAtURL` over the existing `atProxyURLs` and `plainProxyURLs`: `ShownProxyURL(raw) == ProxyURLHidden` and `ProxyURLHasAt(raw)` for every at-url, `ShownProxyURL(raw) == raw` and `!ProxyURLHasAt(raw)` for every plain one, and `ProxyURLHidden == "(hidden: the url contains an @)"`.
  - `internal/depcheck/depcheck_test.go`: add `"internal/urlrule"` to `sessionLibraries`; add
    ```go
    // modeLeaves are the session-safe leaves cmd/wt-model will link through
    // internal/modelsetup; none may link internal/modelclient (sessionBanned).
    var modeLeaves = []string{"internal/urlrule"}
    ```
    and `TestModeLeavesStayOffTheClient`: for each `pkg` in `modeLeaves`, `t.Run(pkg, …)` and `t.Errorf("%s links internal/modelclient; move the shared code down into %s instead", pkg, pkg)` when `closure(t, pkg)["internal/modelclient"]`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/urlrule ./internal/depcheck -count=1` — expect `no Go files`/`undefined: Hidden` for urlrule and `go list -deps internal/urlrule … cannot find package` from depcheck.
- [ ] **Step 3: Implement** `urlrule.go` with the three names above (the comment block from `modelclient/urlcreds.go:5-13` becomes its package doc, naming both the model-server url and the LiteLLM pair url); turn `modelclient/urlcreds.go:15-24` and `config/proxyurl.go:18-37` into the delegations above (config's comment :8-16 now says the rule lives in `urlrule`, with no second copy). Docs: add `urlrule` to wt `CLAUDE.md`'s package list (:94) and to the session library list in its ratchet paragraph (:307) with one sentence: "`TestModeLeavesStayOffTheClient` keeps the session-safe leaves `cmd/wt-model` will link through `modelsetup` off `modelclient`."; in `docs/internals/packages.md` add a row `internal/urlrule/` ("the one url `@` rule: `HasUserinfo`, `Hidden`, `Shown`; stdlib only; `config`'s pair-url names and `modelclient`'s delegate to it") and the same sentence to the rule bullets (:91).
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session.
- [ ] **Step 5: Commit** `refactor(urlrule): give the url @ rule one definition` — the files above.

### Task 2: `internal/modelping` types: Endpoint, the typed errors, Clean, Addr

**Files:**
- Create: `internal/modelping/endpoint.go`, `internal/modelping/errors.go`, `internal/modelping/errors_test.go`; `git mv internal/modelclient/clean_test.go internal/modelping/clean_test.go`
- Modify: `internal/modelclient/endpoint.go:12-21`, `internal/modelclient/errors.go:10-68,75-85`, `internal/modelclient/urlcreds.go:26-42`, `internal/modelclient/clean.go:8-25`, `internal/modelclient/client.go:85-97` (addr), `internal/depcheck/depcheck_test.go`, wt `CLAUDE.md` (:94, :307), `docs/internals/packages.md`
- Test: `internal/modelping/errors_test.go`, `internal/modelping/clean_test.go`, `internal/modelclient/errors_test.go` (new)

**Interfaces:**
- Consumes: `urlrule.Hidden`, `urlrule.HasUserinfo` (Task 1).
- Produces (imports `modelapi`, `urlrule`, stdlib):
  ```go
  type Endpoint struct{ URL, Token string }            // from modelclient/endpoint.go:13
  func (e Endpoint) Origin() string                    // :18
  func (e Endpoint) Gateway() (origin, token string)   // :21
  const UnparsedAddr = "(the configured url, which names no host)"                          // urlcreds.go:38
  const UnbuildableRequest = "the model server url does not parse as a url with a host"     // urlcreds.go:42
  func Addr(rawURL string) string   // from (*Client).addr, client.go:89-97: urlrule.Hidden when HasUserinfo, else url.Parse(trimmed origin).Host, else UnparsedAddr
  func Clean(s string) string       // from modelclient/clean.go:14
  type UnreachableError struct{ Addr, Reason string }
  type UnauthorizedError struct{ Addr string }
  type SkewError struct{ Addr string; Server, Client int }
  type SchemaError struct{ Addr string; Server, Client int }
  type NotModelServerError struct{ Addr, Why string }
  type ResponseTooLargeError struct{ Addr string; Limit int }
  type CredentialsInURLError struct{}
  ```
  Error texts byte for byte from `modelclient/errors.go:13-68,83-85` and `urlcreds.go:32-34` (`clean(` → `Clean(`, `HiddenURL` → `urlrule.Hidden`). `modelclient` keeps, as aliases: `type Endpoint = modelping.Endpoint`, `type UnreachableError = modelping.UnreachableError` (and Unauthorized, Skew, Schema, NotModelServer, ResponseTooLarge, CredentialsInURL), `const unparsedAddr = modelping.UnparsedAddr`, `const unbuildableRequest = modelping.UnbuildableRequest` (in-package tests use both), `func clean(s string) string { return modelping.Clean(s) }`, `CleanForTest` unchanged (held by `modelserver/client_e2e_test.go:95`), `func (c *Client) addr() string { return modelping.Addr(c.ep.URL) }`. `UnknownModelError`, `GatewayDownError`, `IsCode`, `NotConfiguredError`, `Still`, `ResolveEndpoint` stay in modelclient.

- [ ] **Step 1: Write the failing tests.**
  - `internal/modelping/errors_test.go`:
    - `TestErrorTextsAreByteForByte` (table, `got := err.Error()` must equal):
      - `&UnreachableError{Addr: "127.0.0.1:8787", Reason: "connection refused"}` → `"model server at 127.0.0.1:8787 is not reachable (connection refused) — start it with 'wt-model serve --listen 127.0.0.1:8787 --token-file PATH', or fix url under [model-server] in config.toml"`
      - `&UnreachableError{Addr: urlrule.Hidden}` → `(&CredentialsInURLError{}).Error()`
      - `&UnreachableError{Addr: UnparsedAddr, Reason: UnbuildableRequest}` → `UnbuildableRequest + " (it is not shown) — write it as http://HOST:PORT under [model-server] in config.toml, or in WT_MODEL_URL"`
      - `&UnauthorizedError{Addr: "h:1"}` → `"model server at h:1 refused the token (" + modelapi.MsgUnauthorized + ") — check token_file under [model-server] in config.toml and that the server was started with the same token"`
      - `&SkewError{"h:1", 2, 1}` → `"model server at h:1 speaks API major 2, this wt speaks 1 — upgrade wt"`; `&SkewError{"h:1", 0, 1}` → `"… speaks API major 0, this wt speaks 1 — upgrade wt-model"`
      - `&SchemaError{"h:1", 1, 2}` → `"model server at h:1 speaks catalog schema 1, this wt needs 2 — restart wt-model (it is older than this wt)"`; `&SchemaError{"h:1", 3, 2}` → `"model server at h:1 sends catalog schema 3, this wt reads up to 2 — upgrade wt"`
      - `&NotModelServerError{"h:1", "x"}` → `"h:1 is not a wt-model server (x) — check url under [model-server] in config.toml"`
      - `&ResponseTooLargeError{"h:1", 32 << 20}` → `"model server at h:1 sent an answer larger than 32 MiB — this wt reads answers up to that size"`
      - `&CredentialsInURLError{}` → the text of `modelclient/urlcreds.go:33`.
      - and `&NotModelServerError{Addr: "h:1", Why: "a\nb‮"}` holds no `\n` and no U+202E (Clean applies).
    - `TestAddrNeverEchoesTheURL`: `Addr("http://u:p@h:1") == urlrule.Hidden`; `Addr("hostzz9") == UnparsedAddr`; `Addr("http://127.0.0.1:8787/x/") == "127.0.0.1:8787"`.
    - `TestEndpointOriginAndGateway`: `Endpoint{URL: "http://h:1//", Token: "t"}.Origin() == "http://h:1"`, `Gateway()` returns `("http://h:1", "t")`.
  - `internal/modelping/clean_test.go` (git mv): package `modelping`, `clean(` → `Clean(`; test name `TestCleanDropsControlsAndBidi` kept.
  - `internal/modelclient/errors_test.go` (new) `TestClientErrorsAreModelpingErrors`: `errors.As(error(&UnreachableError{Addr: "a"}), new(*modelping.UnreachableError))` and the same for `SkewError`, `CredentialsInURLError`; and `Endpoint{URL: "u"}` is assignable to `modelping.Endpoint` (`var _ modelping.Endpoint = Endpoint{}`) — pins that callers' `errors.As(err, &*modelclient.X)` keeps working.
  - depcheck: `"internal/modelping"` added to `sessionLibraries` and `modeLeaves`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelping ./internal/modelclient ./internal/depcheck -count=1` — expect `undefined: UnreachableError` (modelping), `undefined: modelping` (modelclient) and the depcheck `cannot find package`.
- [ ] **Step 3: Implement** `modelping/endpoint.go` (Endpoint, Origin, Gateway, Addr, UnparsedAddr, UnbuildableRequest, Clean) and `modelping/errors.go` (the seven types, texts moved byte for byte); replace the modelclient definitions with the aliases above (`errors.go` keeps only `UnknownModelError`, `GatewayDownError`, `IsCode`; `urlcreds.go` keeps only `HiddenURL`, `HasUserinfo` and the `CredentialsInURLError` alias; `endpoint.go:12-21` becomes the alias). Docs: `modelping` row in `packages.md` ("one ping and its classification; `Endpoint`, the ping errors (aliased by `modelclient`), `Clean`, `Addr`; imports `modelapi`, `urlrule`"), package list and ratchet paragraph in wt `CLAUDE.md`.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session.
- [ ] **Step 5: Commit** `refactor(modelping): move the endpoint, the ping errors and clean below modelclient` — the files above.

### Task 3: `modelping` Send, CheckPing, Ping, Classify, Checked, GetJSON; modelclient delegates

**Files:**
- Create: `internal/modelping/ping.go`, `internal/modelping/classify.go`, `internal/modelping/ping_test.go`, `internal/modelping/classify_test.go`
- Modify: `internal/modelclient/client.go:21-29` (consts), `:66-83` (New), `:107-185` (response, send), `:187-200`, `:205-232`, `:242-279` (Ping), `:416`, `:449-473` (field renames)
- Test: the two new files; `internal/modelclient/client_test.go` and `urlcreds_test.go` unchanged (they are the net for the delegation)

**Interfaces:**
- Consumes: Task 2's names.
- Produces:
  ```go
  const (ServerName = "wt-model"; DefaultTimeout = 10 * time.Second; DialTimeout = 5 * time.Second; MaxResponse = 32 << 20) // client.go:22-28
  func NewHTTPClient() *http.Client  // client.go:73-80: cloned DefaultTransport, Proxy nil, DialContext with DialTimeout, CheckRedirect → http.ErrUseLastResponse
  type Response struct{ Status int; Header http.Header; Body []byte }
  func Send(ctx context.Context, hc *http.Client, timeout time.Duration, ep Endpoint, method, rawurl string, body any) (*Response, error) // client.go:121-178 + the return of :184, without the Date offset
  func CheckPing(addr string, r *Response) (modelapi.Ping, error) // client.go:256-274; returns the decoded Ping beside *SkewError / *SchemaError
  func Ping(ctx context.Context, ep Endpoint) (modelapi.Ping, error) // Send(ctx, NewHTTPClient(), DefaultTimeout, ep, "GET", ep.Origin()+"/control/v1/ping", nil) then CheckPing(Addr(ep.URL), r)
  func GetJSON(ctx context.Context, ep Endpoint, path string, out any) error
  type Kind int
  const (KindNone Kind = iota; KindUnreachable; KindUnauthorized; KindNotWtModel; KindSkew; KindSchema) // same names and order as modelsetup/observe.go:118-125
  type Outcome struct{ Kind Kind; Message string; API, Schema int }
  func Classify(err error) Outcome  // modelsetup/observe.go:629-648: nil → zero Outcome; Message = err.Error(); Skew → API = Server; Schema → Schema = Server; unknown type → KindUnreachable
  func Checked(ctx context.Context, ep Endpoint) (modelapi.Ping, Kind, string, error) // Ping; nil → (p, KindNone, "", nil); else o := Classify(err) → (p, o.Kind, o.Message, err)
  ```
  `GetJSON`: `Ping` first (its error returned as is); then `Send` GET `ep.Origin()+path`; 401 → `*UnauthorizedError{Addr}`; 3xx → `*NotModelServerError{Why: "it answered HTTP <n>, a redirect, which wt does not follow"}`; other non-2xx → `*NotModelServerError{Why: "GET <path> answered HTTP <n>"}`; undecodable → `*NotModelServerError{Why: "its answer to GET <path> is not the expected JSON"}` (the words of `client.go:190,214`). First caller is PR 4d; it ships here because the spine puts it in `modelping`.
  modelclient: `type response = modelping.Response` (field uses `r.status/.header/.body` → `r.Status/.Header/.Body` at client.go:189-229, :256-263, :416, :449-473); `New` uses `modelping.NewHTTPClient()` when `c.hc == nil`; `(*Client).send` = `modelping.Send(ctx, c.hc, c.timeout, c.ep, method, rawurl, body)` then the Date-offset block (:179-183) on success; `(*Client).Ping` keeps its once-per-client cache (:245-251, :275-277) and calls `modelping.CheckPing(c.addr(), r)`. `defaultRequestTimeout`, `dialTimeout`, `maxResponse`, `serverName` become `= modelping.DefaultTimeout` / `DialTimeout` / `MaxResponse` / `ServerName` (client_test.go:230,235 read `maxResponse`).

- [ ] **Step 1: Write the failing tests.**
  - `internal/modelping/ping_test.go`:
    - `TestCheckPingClassifiesEachAnswer` — `CheckPing("h:1", &Response{Status: s, Body: []byte(b)})`: 401 → `*UnauthorizedError{Addr: "h:1"}`; 404 → `*NotModelServerError` with `Why == "GET /control/v1/ping answered HTTP 404"`; 307 → Why `"GET /control/v1/ping answered HTTP 307"`; 200 `"nope"` → Why `"its ping answer is not JSON"`; 200 `{"api_major":<APIMajor>,"schema":<SchemaVersion>,"server":"other"}` → Why `` `it identifies as "other"` ``; 200 with `api_major` APIMajor+1 → `*SkewError{Server: APIMajor+1, Client: APIMajor}` and the returned `Ping.APIMajor == APIMajor+1`; 200 with schema SchemaVersion+1 → `*SchemaError{Server: SchemaVersion+1, Client: SchemaVersion}` and `Ping.Schema == SchemaVersion+1`; 200 good → nil and `Ping{APIMajor, SchemaVersion, "wt-model"}`.
    - `TestSendRefusesAnAtURLBeforeAnyRequest` — an `httptest` server counting requests; `Send` with `Endpoint{URL: "http://u:p@" + host}` → `errors.As(err, new(*CredentialsInURLError))`, count 0.
    - `TestSendHostlessURLQuotesNothing` — `Endpoint{URL: "hostzz9"}` → `*UnreachableError{Addr: UnparsedAddr, Reason: UnbuildableRequest}`, text has no `hostzz9`.
    - `TestSendNamesTheReadLimit` — server writes `MaxResponse+1` bytes → `*ResponseTooLargeError{Limit: MaxResponse}`.
    - `TestSendTimeoutSaysTimedOut` — handler blocks until the test ends, `timeout = 50*time.Millisecond` → `*UnreachableError` whose `Reason` starts `"timed out after "`.
    - `TestSendCancelledContextIsTheContextError` — cancelled ctx → `errors.Is(err, context.Canceled)` and not `*UnreachableError`.
    - `TestSendSendsTheBearerAndJSONHeaders` — `Authorization: Bearer t`, `Accept: application/json`, and `Content-Type: application/json` only when `body != nil`.
    - `TestNewHTTPClientIgnoresEnvProxyAndRedirects` — `NewHTTPClient().Transport.(*http.Transport).Proxy == nil`; `CheckRedirect(nil, nil) == http.ErrUseLastResponse`.
    - `TestPingAgainstAServer` — good ping server → Ping with `Server == "wt-model"`; a closed listener → `*UnreachableError` whose text contains `"is not reachable ("`.
    - `TestGetJSONPingsFirstThenDecodes` — server answers ping and `/x` with `{"a":1}`: paths seen in order `["/control/v1/ping", "/x"]`, `out["a"] == 1.0`; `/x` 401 → `*UnauthorizedError`; 500 → Why `"GET /x answered HTTP 500"`; body `"{"` → Why `"its answer to GET /x is not the expected JSON"`.
  - `internal/modelping/classify_test.go`:
    - `TestClassifyMapsEachPingError` (replaces modelsetup's `TestObserveClassifiesEachPingFailure` in Task 8): nil → `Outcome{}`; `&UnreachableError{"a","refused"}` → KindUnreachable; `&UnauthorizedError{"a"}` → KindUnauthorized; `&NotModelServerError{"a","x"}` → KindNotWtModel; `&SkewError{"a",2,1}` → KindSkew, `API == 2`; `&SchemaError{"a",2,1}` → KindSchema, `Schema == 2`; `fmt.Errorf("ctx: %w", &UnauthorizedError{"a"})` → KindUnauthorized; `errors.New("x")` → KindUnreachable; `&CredentialsInURLError{}` → KindUnreachable; for every non-nil case `Message == err.Error()`.
    - `TestCheckedReturnsTheClassifiedFailure` — closed listener → `(_, KindUnreachable, msg, err)` with `msg == err.Error()`, err non-nil; good server → `(p, KindNone, "", nil)`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelping -count=1` — expect `undefined: CheckPing`, `undefined: Classify`.
- [ ] **Step 3: Implement** `ping.go` (consts, NewHTTPClient, Response, Send, CheckPing, Ping, GetJSON) and `classify.go` (Kind, Outcome, Classify, Checked), moving the bodies named above; rewire `modelclient/client.go` as listed (no behaviour change: the client still caches the ping, keeps its Date offset and its caller-supplied `hc`).
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session; `go test ./internal/modelclient -count=1 -v -run 'Ping|Redirect|Unreachable|Unauthorized|ReadLimit|Timeout|AtURL|Hostless|Unbuildable' | grep -c '^--- PASS'` equals the count before the task (run it once on the parent commit first).
- [ ] **Step 5: Commit** `refactor(modelping): one ping, its transport and its classification; modelclient delegates` — the files above.
- [ ] **PR 1a gate** (Exit gate above), then open PR 1a: title `refactor(wt): urlrule and modelping leaves under modelclient (phase 4 stage 2, PR 1a)`; body names the design (`docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md`), says "no behaviour change", lists the deleted `TestProxyURLRuleIsModelclientsRule` and why, and uses `Refs`, never a closing keyword.

---

### PR 1b: `modelmode` mode rules, words, steps and seams

### Task 4: `modelmode` mode rules (moved from modelclient)

**Files:**
- Move: `git mv internal/modelclient/mode.go internal/modelmode/mode.go`; `git mv internal/modelclient/mode_test.go internal/modelmode/mode_test.go`
- Create: `internal/modelclient/mode.go` (aliases), `internal/modelclient/mode_test.go` (the two tests that stay, below)
- Modify: `internal/depcheck/depcheck_test.go`, wt `CLAUDE.md` (:94, :111 names `modelclient.ResolveMode` — keep the name, it is an alias; :307), `docs/internals/packages.md:76` (the `modelclient/mode.go` row becomes `internal/modelmode/`)

**Interfaces:**
- Consumes: nothing new.
- Produces (`modelmode`, stdlib only at this task), whole file from `modelclient/mode.go:8-106`:
  ```go
  type Mode string; const (ModeNative Mode = "native"; ModeLocal Mode = "local"; ModeRemote Mode = "remote")
  func ParseMode(s string) (Mode, bool)
  type ModeSource int; const (SourceNone ModeSource = iota; SourceEnvMode; SourceEnvEndpoint; SourceStored; SourceInferred)
  type Resolution struct{ Mode Mode; Source ModeSource; Problem string }
  func ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution
  type ModeUnsetError struct{}; type NativeModeError struct{ Source ModeSource }; type ModeProblemError struct{ Problem string }
  func IsModeUnset(err error) bool; func IsNativeMode(err error) bool
  ```
  `modelclient/mode.go`: `type Mode = modelmode.Mode`; `const (ModeNative = modelmode.ModeNative; ModeLocal = modelmode.ModeLocal; ModeRemote = modelmode.ModeRemote; SourceNone = modelmode.SourceNone; SourceEnvMode = …; SourceEnvEndpoint = …; SourceStored = …; SourceInferred = …)`; `type ModeSource = modelmode.ModeSource`, `Resolution`, `ModeUnsetError`, `NativeModeError`, `ModeProblemError` likewise; `func ParseMode(s string) (Mode, bool) { return modelmode.ParseMode(s) }`, `ResolveMode`, `IsModeUnset`, `IsNativeMode` one-line wrappers; `NoServer` (:110) stays here (it needs `NoServerConfigured`).

- [ ] **Step 1: Write the failing tests.** After the two `git mv`s, set `package modelmode` in both moved files. In `modelmode/mode_test.go` keep `TestResolveModeTable`, `TestModeErrorsKeepTheExistingLead`, `TestModeUnsetMessageNamesTheFix`, `TestParseModeTrimsAndFolds`; cut `TestErrorsAreRecognisedThroughWrapping` and `TestNoServerCoversUnsetNativeAndLegacy` (they use `Still`, `NoServer`, `NotConfiguredError`, which stay in modelclient) into the new `modelclient/mode_test.go`, unchanged. Add to it `TestModeNamesAreModelmodeNames`: `var _ modelmode.Resolution = ResolveMode("", "", "", func(string) string { return "" })`; `errors.As(Still(&modelmode.NativeModeError{}, "x"), new(*NativeModeError))`; `ModeLocal == modelmode.ModeLocal`. depcheck: add `"internal/modelmode"` to `sessionLibraries` and `modeLeaves`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelclient ./internal/modelmode -count=1` — expect `undefined: ResolveMode` in modelclient and `undefined: modelmode`.
- [ ] **Step 3: Implement** the alias file `modelclient/mode.go` above; docs rows.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session (the gate and native tests use `modelclient.ResolveMode` through the alias).
- [ ] **Step 5: Commit** `refactor(modelmode): move the mode rules below modelclient` — the files above.

### Task 5: `modelmode` Secret, refusals, url words, paths, Step and Result

**Files:**
- Create: `internal/modelmode/secret.go`, `internal/modelmode/plan.go`, `internal/modelmode/urlcreds.go`, `internal/modelmode/paths.go`, `internal/modelmode/apply.go`, `internal/modelmode/plan_test.go`, `internal/modelmode/urlcreds_test.go`
- Modify: `internal/modelsetup/plan.go:18-47` (Secret), `:85-150` (StepKind/StepAction/Step), `:167-177` (RefusalError, reason), `:267-273` (credentialsReason); `internal/modelsetup/apply.go:42-54` (Result), `:119-158` (fail, stepLabel); `internal/modelsetup/observe.go:547-557` (token paths); `internal/modelsetup/env.go:35-36` (DefaultListen); `internal/modelsetup/urlcreds.go:20-27,40-84`; every `hasUserinfo(`/`shownURL(`/`redactCheck(`/`hiddenURL`/`notCheckedAtURL`/`checkHidden` in `internal/modelsetup/*.go` and `*_test.go`; `internal/modelsetup/plan_test.go:571-589` (moves)
- Test: `internal/modelmode/plan_test.go`, `internal/modelmode/urlcreds_test.go`; the modelsetup suites unchanged otherwise

**Interfaces:**
- Consumes: `urlrule` (Task 1).
- Produces:
  ```go
  type Secret string // String, GoString, MarshalJSON, MarshalText (all "[redacted]", "" for empty), Reveal — plan.go:23-47
  type RefusalError struct{ Reasons []string }      // Error() = strings.Join(Reasons, "\n")
  func Reason(fact, wayOut string) string           // fact + "; " + wayOut + "; nothing was changed"
  func CredentialsReason(what string) string        // Reason(what+" contains an @ (read as a user:password); wt does not store credentials in config.toml", "use a token file")
  const NotCheckedAtURL = "not checked: the url contains an @; retype it without credentials"
  const CheckHidden = "the check failed; the url contains an @ so the error text is hidden"
  func ShownURL(raw string) string { return urlrule.Shown(raw) }
  func RedactCheck(msg string, urls ...string) string // urlcreds.go:66-84, hasUserinfo → urlrule.HasUserinfo
  const DefaultListen = "127.0.0.1:8787"
  func DefaultLocalTokenPath(configHome string) string // filepath.Join(configHome, "wt-model", "server.token")
  func RemoteTokenPath(configHome string) string       // filepath.Join(configHome, "agent-wt", "remote-model-server.token")
  type StepKind string; type StepAction string
  const (StepToken StepKind = "token"; StepConfig StepKind = "config")
  const (ActionTightenToken StepAction = "tighten-token"; ActionWritePastedToken StepAction = "write-pasted-token"; ActionReplacePastedToken StepAction = "replace-pasted-token")
  type Step struct{ Kind StepKind; Path, Detail string; Action StepAction; Value string }
  type Result struct{ Done []Step; Failed *Step; Err error; Warnings, Notes []string; Recovery string }
  func StepLabel(k StepKind) (string, bool) // StepToken → "token file"; StepConfig → "write [model-server]"; else ("", false)
  ```
  modelsetup: `type Secret = modelmode.Secret`; `type RefusalError = modelmode.RefusalError`; `type StepKind = modelmode.StepKind`; `type StepAction = modelmode.StepAction`; `type Step = modelmode.Step`; `type Result = modelmode.Result`; `const (StepToken = modelmode.StepToken; StepConfig = modelmode.StepConfig)`; `const (ActionTightenToken = modelmode.ActionTightenToken; ActionWritePastedToken = …; ActionReplacePastedToken = …)`; its other kinds (`StepPair` … `StepKickstartLiteLLM`) and actions (`ActionGenerateToken`, `ActionPairKey*`, `ActionWaitLiteLLM`) stay in plan.go typed `StepKind`/`StepAction` (now the modelmode types); `const DefaultListen = modelmode.DefaultListen`; `func DefaultLocalTokenPath(h string) string { return modelmode.DefaultLocalTokenPath(h) }` and `RemoteTokenPath` likewise; `func reason(fact, wayOut string) string { return modelmode.Reason(fact, wayOut) }` (a one-line delegation, ~40 call sites stay); `credentialsReason` deleted, its two call sites (plan.go:293, :401) call `modelmode.CredentialsReason`; `(r Result) fail` (apply.go:119) becomes `func failed(r Result, s Step, err error) Result` (methods cannot be declared on an alias of another package's type); `stepLabel` handles its own kinds and ends `if l, ok := modelmode.StepLabel(k); ok { return l }; return "unknown step"`. `urlcreds.go` keeps only `notCheckedPairAtURL` and `pairErrHidden`; call sites become `urlrule.HasUserinfo`, `modelmode.ShownURL`, `modelmode.RedactCheck`, `urlrule.Hidden`, `modelmode.NotCheckedAtURL`, `modelmode.CheckHidden` (tests too: `gofmt -r 'hasUserinfo(x) -> urlrule.HasUserinfo(x)' -w`, likewise for the other two functions; the three constants by search and replace; then add the imports).

- [ ] **Step 1: Write the failing tests.**
  - Move `TestSecretRedactsEveryPrintedForm` (modelsetup/plan_test.go:571-589) to `modelmode/plan_test.go`, with `const plPasted = "tok-PASTED-SECRET-42"` there.
  - `modelmode/plan_test.go`: `TestReasonSaysNothingWasChanged`: `Reason("a", "b") == "a; b; nothing was changed"`; `CredentialsReason("the url") == "the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; nothing was changed"`; `(&RefusalError{Reasons: []string{"x", "y"}}).Error() == "x\ny"`. `TestStepLabelKnowsOnlyTheSessionKinds`: `StepLabel(StepToken) == ("token file", true)`, `StepLabel(StepConfig) == ("write [model-server]", true)`, `StepLabel("plist") == ("", false)`, `StepLabel("bootout") == ("", false)`.
  - `modelmode/urlcreds_test.go`: `TestRedactCheckFailsClosed`: `RedactCheck("") == ""`; `RedactCheck("x@y") == CheckHidden`; `RedactCheck("dial abc: no host", "http://abc@h") == CheckHidden` (a 3-byte run of an at-url); `RedactCheck("plain words", "http://h:1", "http://abc@h") == "plain words"`. `TestTokenPathsAndDefaultListen`: `DefaultLocalTokenPath("/c") == "/c/wt-model/server.token"`, `RemoteTokenPath("/c") == "/c/agent-wt/remote-model-server.token"`, `DefaultListen == "127.0.0.1:8787"`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode -count=1` — expect `undefined: Secret`, `undefined: Reason`.
- [ ] **Step 3: Implement** the modelmode files (bodies moved from the lines above) and the modelsetup aliases and call-site renames.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; `go test ./internal/modelsetup -count=1 -run 'Golden|Format|Secret|Refus|URL|AtURL|Unknown' -v | grep -c '^--- FAIL'` prints 0; G-session.
- [ ] **Step 5: Commit** `refactor(modelmode): move the secret, refusal, url words, token paths and step types down` — the files above.

### Task 6: `modelmode` seams and the real machine; modelsetup widens them and stops importing modelclient

**Files:**
- Create: `internal/modelmode/env.go`, `internal/modelmode/env_test.go`, `internal/modelmode/testmain_test.go`
- Modify: `internal/modelsetup/env.go:8-30` (imports), `:39-100` (FS, ConfigStore, Net, Host), `:112-136` (Env, DefaultEnv), `:138-254` (guards, realFS), `:400-413` (realConfig, realNet.Ping), `:498-518` (realHost); `internal/modelsetup/observe.go:17,51-53,115-125,215 (`Resolution modelclient.Resolution` → `modelmode.Resolution`; Task 7 rewrites State, but Step 3's import check needs it gone now),291,362,489,518,526,574-584,620-650`; `internal/modelsetup/plan.go:15,55,201-227,590`; `internal/modelsetup/apply.go:18,396`; `internal/modelsetup/fakes_test.go:18,350-359`; `internal/modelsetup/env_test.go:51,116,136,145,155,183,200-327`; `internal/modelsetup/finalreview_test.go:216-251`; `internal/configeditor/server_aturl_test.go:21-28`
- Test: `internal/modelmode/env_test.go`; `internal/modelsetup/env_test.go`

**Interfaces:**
- Consumes: Task 2 to 5.
- Produces (`modelmode` now imports `config`, `modelapi`, `modelping`, `urlrule`):
  ```go
  type FS interface {
      ReadFile(path string) ([]byte, error); Stat(path string) (fs.FileInfo, error); Lstat(path string) (fs.FileInfo, error)
      WriteAtomic(path string, data []byte, perm os.FileMode) error; Chmod(path string, perm os.FileMode) error
  }
  type ConfigStore interface {
      Read() config.ModelServerFile
      Update(change func(config.ModelServerState) (config.ModelServerState, error)) error
  }
  type Net interface {
      Ping(ctx context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error)
  }
  type Host interface{ Getenv(string) string; Home() (string, error) }
  type Env struct{ FS FS; Config ConfigStore; Net Net; Host Host }
  func DefaultEnv() Env // Env{RealFS{}, RealConfig{}, RealNet{}, RealHost{}}
  var ErrOutsideTempUnderTest = errors.New("a test binary changes no file outside the temp directory") // env.go:144, exported so modelsetup's tests keep errors.Is
  func GuardUnderTest(paths ...string) error                  // env.go:148-169
  type RealFS struct{ Resolve func(string) (string, error) }  // env.go:171-183 (field exported)
  func (x RealFS) GuardAncestorUnderTest(p string) error       // env.go:189-202
  // RealFS: ReadFile, Stat, Lstat, Chmod, WriteAtomic — env.go:204-221, :231-254
  type RealConfig struct{}  // Read, Update — env.go:402-405
  type RealNet struct{}     // Ping = modelping.Checked
  type RealHost struct{}    // Getenv, Home with the test-home refusal — env.go:500-518
  func DanglingLink(f FS, p string) bool // observe.go:577-584, over the FS seam
  ```
  modelsetup (env.go): `type FS interface{ modelmode.FS; Remove(path string) error; Backup(path, dir string) (dst string, tightened bool, err error) }`; `type ConfigStore interface{ modelmode.ConfigStore; Pair() (config.ProxyPair, error); SetPair(url, key *string) error }`; `type Net interface{ modelmode.Net; Get(…); ListenerPID(…); ProcessStart(…) }` (signatures of :84-86); `type Host interface{ modelmode.Host; Executable() (string, error); LookPath(string) (string, error); Now() time.Time; Sleep(ctx context.Context, d time.Duration) error; RandRead([]byte) error; GOOS() string; UID() int }`; `Env` keeps its seven fields; `func (e Env) Mode() modelmode.Env { return modelmode.Env{FS: e.FS, Config: e.Config, Net: e.Net, Host: e.Host} }`. Real impls embed: `type realFS struct{ modelmode.RealFS }` (+ `Remove` :222-230 and `Backup` :275-331 unchanged in place, calling `modelmode.GuardUnderTest` and `x.GuardAncestorUnderTest`; `Remove` stays in `modelsetup/env.go`, so `internal/removal/deletionscan_test.go:42`'s entry and its count of 1 are unchanged), `type realConfig struct{ modelmode.RealConfig }` (+ Pair, SetPair), `type realNet struct{ modelmode.RealNet }` (+ Get, ListenerPID, ProcessStart), `type realHost struct{ modelmode.RealHost }` (+ the seven others). `errUnderTest` (launchctl/lsof/ps) stays in modelsetup. `type ServerKind = modelping.Kind` with `const (KindNone = modelping.KindNone; KindUnreachable = …; KindUnauthorized = …; KindNotWtModel = …; KindSkew = …; KindSchema = …)` replaces observe.go:115-125 (early: the new seam returns `modelping.Kind`). `pingServer` (observe.go:622) becomes: `p, kind, msg, err := env.Net.Ping(ctx, ep)`; `err == nil` → `OK, API, Schema = true, p.APIMajor, p.Schema`; else `Kind, Message = kind, msg`, and `API = p.APIMajor` when `kind == KindSkew`, `Schema = p.Schema` when `kind == KindSchema` (CheckPing returns the decoded ping beside those two errors). Every remaining `modelclient.` in modelsetup production code becomes `modelmode.` (Mode, ParseMode, ResolveMode, ModeLocal/Remote/Native) or `modelping.Endpoint`; the `modelclient` import leaves modelsetup's non-test files.
  Fakes: `fakeNet.Ping(_ context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error)` records as today, takes `err` as today, `o := modelping.Classify(err)`, returns `p, o.Kind, o.Message, err` where `p := x.f.ping` with `p.APIMajor = o.API` when `o.Kind == modelping.KindSkew` and `p.Schema = o.Schema` when `o.Kind == modelping.KindSchema` — so every typed error the tests build keeps today's State. `configeditor/server_aturl_test.go`'s `recNet.Ping` gets the same 4-value signature (`err := &modelping.UnreachableError{…}; o := modelping.Classify(err); return modelapi.Ping{}, o.Kind, o.Message, err`).

- [ ] **Step 1: Write the failing tests.**
  - `modelmode/testmain_test.go`: `TestMain` with `config.IsolateConfigHomeForTest()` exactly as `modelsetup/testmain_test.go:12-17`.
  - Move to `modelmode/env_test.go` (rename `realFS{` → `RealFS{`, `resolve:` → `Resolve:`, `realHost{` → `RealHost{`, `errOutsideTempUnderTest` → `ErrOutsideTempUnderTest`): `TestRealWriteAtomicKeepsASymlink`, `TestRealWriteAtomicGuardsTheResolvedPath`, `TestRealChmodGuardsTheResolvedPath`, `TestRealWriteAtomicRefusesAnUnresolvableSymlink` (from modelsetup/env_test.go), `TestRealHostHomeRefusesTheDevelopersHomeUnderTest` (finalreview_test.go:237-251), and `TestRealFSRefusesPathsOutsideTheTempDirUnderTest` (finalreview_test.go:216-235) with its `WriteAtomic` and `Chmod` rows; modelsetup keeps that test with only the `Remove` row (its `realFS{}`; name kept).
  - New in `modelmode/env_test.go`: `TestDefaultEnvIsTheRealMachine`: `DefaultEnv()` has `FS.(RealFS)`, `Config.(RealConfig)`, `Net.(RealNet)`, `Host.(RealHost)`. `TestDanglingLinkIsALinkStatCannotFollow`: in `t.TempDir()`, `os.Symlink` to a missing target → `DanglingLink(RealFS{}, link)` true; a regular file → false; a missing path → false.
  - New in `modelsetup/env_test.go`: `TestEnvModeIsTheNarrowView`: `e := newFakeEnv(t).Env(); m := e.Mode()`; `m.FS == e.FS`, `m.Config == e.Config`, `m.Net == e.Net`, `m.Host == e.Host` (interface equality of the same fake values). Remaining env tests in modelsetup keep `realFS{}` (the zero value is still valid) and change only `realFS{resolve: f}` → `realFS{modelmode.RealFS{Resolve: f}}`, `errOutsideTempUnderTest` → `modelmode.ErrOutsideTempUnderTest`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode ./internal/modelsetup -count=1` — expect `undefined: RealFS`, `undefined: DefaultEnv`, `e.Mode undefined`.
- [ ] **Step 3: Implement** `modelmode/env.go` and the modelsetup changes above (realFS keeps Remove and Backup in `modelsetup/env.go`). Verify the import edge: `go list -f '{{join .Imports "\n"}}' ./internal/modelsetup | grep -c internal/modelclient` prints 0.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast (it includes `./internal/removal`: the deletion scan still finds one `os.Remove` in `internal/modelsetup/env.go` and none in `internal/modelmode`); G-session; `go test ./internal/configeditor -count=1 -run 'AtURL|Credential|Server' -v | grep -c '^--- FAIL'` prints 0.
- [ ] **Step 5: Commit** `refactor(modelmode): the session-safe seams and the real machine; modelsetup widens them` — the files above.
- [ ] **PR 1b gate** (Exit gate), then open PR 1b: title `refactor(wt): modelmode mode rules, words and seams (phase 4 stage 2, PR 1b)`; body: no behaviour change; `modelsetup` no longer imports `modelclient`; `Host.Home` and `ServerKind` notes; `Refs` the design.

---

### PR 1c: `modelmode` State, Observe, Plan and Apply

Review commit by commit (the spine's (i) types and embeds: Task 7, 9; (ii) Observe: Task 8; (iii) plan: Task 10; (iv) apply: Task 11; (v) tests: Task 12). Record `$TMPDIR/s1-baseline.txt` (see "The safety net") before Task 7.

### Task 7: State and ObserveOptions types; modelsetup.State embeds the session half

**Files:**
- Create: `internal/modelmode/state.go`
- Modify: `internal/modelsetup/observe.go:20-53` (ObserveOptions), `:61-86` (ConfigState, TokenState), `:115-153` (ServerKind, ServerState, HostState), `:212-233` (State), `:301-309` (Host fill), `internal/modelsetup/plan.go:378-379,512,554,645,844,853,893,914` (`st.Host.Binary/BinaryErr/Path/UID` → `st.Tools.*`); tests: `internal/modelsetup/observe_test.go:261-270`, `plan_test.go:38-58,473,890`, every `ObserveOptions{<field>:` literal in `internal/modelsetup/*_test.go` (16), `internal/configeditor/server_tab_test.go:171-200,204-212,549-556,1211-1220`, `internal/configeditor/server_litellm_test.go:~25-35`, and any other `modelsetup.State{` literal naming `Resolution`, `EnvOverride`, `Config`, `Token`, `TokenFiles`, `Server` or `Host`
- Test: `internal/modelsetup/observe_test.go`

**Interfaces:**
- Consumes: Task 4 to 6.
- Produces (`modelmode/state.go`):
  ```go
  type ServerKind = modelping.Kind
  const (KindNone = modelping.KindNone; KindUnreachable = modelping.KindUnreachable; KindUnauthorized = modelping.KindUnauthorized; KindNotWtModel = modelping.KindNotWtModel; KindSkew = modelping.KindSkew; KindSchema = modelping.KindSchema)
  type ConfigState struct{ Path string; Exists bool; ParseErr, TableErr, RestErr string; Table config.ModelServerState; Problems []string; URLHadCredentials bool } // observe.go:67
  type TokenState struct{ Path string; Exists, Loose, Empty bool; Dangling bool; Err string }                                                             // :79
  type ServerState struct{ URL string; Checked, OK bool; Kind ServerKind; Message string; API, Schema int }                                               // :136
  type HostState struct{ Home, ConfigHome string }
  type State struct{ Resolution Resolution; EnvOverride string; Config ConfigState; Token TokenState; TokenFiles []TokenState; Server ServerState; Host HostState }
  type LocalFallback struct{ TokenFile, Listen string }
  type ObserveOptions struct{ TokenPaths []string; CheckURL string; CheckToken Secret; CheckTokenFile string; Local LocalFallback }
  ```
  modelsetup (observe.go): `type ConfigState = modelmode.ConfigState`, `TokenState`, `ServerState`, `HostState` likewise; `ServerKind` and the `Kind*` consts now `= modelmode.X`; `type ToolsState struct{ Binary, BinaryErr, Path string; UID int }`; `type State struct{ modelmode.State; Platform Platform; Pair PairState; Agent AgentState; Listener ListenerState; Tools ToolsState; LiteLLMPlist LiteLLMPlistState; LiteLLMInstall LiteLLMInstallState; LiteLLM LiteLLMState }`; `type ObserveOptions struct{ modelmode.ObserveOptions; ListenPort int; SkipLiteLLMProbe, ProbeLiteLLMWhenLocal bool }` (its `LiteLLMProbeSkipped(mode modelmode.Mode) bool` method stays). `Observe`'s body is unchanged apart from `st.Host.UID/Path/Binary/BinaryErr` → `st.Tools.*` (promoted session fields are assigned as before). Test literals take the form `State{State: modelmode.State{Config: …, Token: …, Host: modelmode.HostState{Home: …, ConfigHome: …}, Server: …}, Platform: …, Tools: ToolsState{Binary: …, Path: …, UID: 501}, Pair: …}` and `ObserveOptions{ObserveOptions: modelmode.ObserveOptions{CheckURL: …, CheckToken: …}}`; configeditor's non-test code assigns promoted fields (`o.CheckURL = …`, `server_tab.go:265-290`) and needs no change.

- [ ] **Step 1: Write the failing test.** Edit `TestObserveFindsTheModelBinaryBesideWtThenOnPath` (observe_test.go:256-275): `st.Host.Binary`/`Path`/`UID`/`BinaryErr` → `st.Tools.Binary`/`Path`/`UID`/`BinaryErr` (`st.Host.Home` stays), and add one line: `if st.State.Host.Home != f.home { t.Errorf(…) }`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelsetup -run TestObserveFindsTheModelBinaryBesideWtThenOnPath -count=1` — expect `st.Tools undefined` and `st.State undefined`.
- [ ] **Step 3: Implement** `modelmode/state.go` and the modelsetup type changes above; rewrite the composite literals listed in Files to the embedded form. Check with `grep -nE 'Host: *(modelsetup\.)?HostState\{[^}]*(Binary|Path:|UID)' internal/modelsetup/*_test.go internal/configeditor/*_test.go` (no output) and `go vet ./internal/...`.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session; the baseline `comm` check (no name missing).
- [ ] **Step 5: Commit** `refactor(modelsetup): State embeds modelmode.State; the admin host facts move to Tools` — the files above.

### Task 8: `modelmode.Observe`; modelsetup.Observe delegates

**Files:**
- Create: `internal/modelmode/observe.go`, `internal/modelmode/observe_test.go`, `internal/modelmode/fakes_test.go`
- Modify: `internal/modelsetup/observe.go:235-530` (Observe), `:532-618` (expandHome, configHome, readToken: move), `:620-650` (pingServer: move); `internal/modelsetup/apply.go:399` (`pingServer` → `modelmode.PingServer(ctx, env.Mode(), ep)`); `internal/modelsetup/plan.go:713-718` (expandIn calls `modelmode.ExpandIn` — see Interfaces); `internal/modelsetup/observe_test.go` (moves below)
- Test: `internal/modelmode/observe_test.go`, `internal/modelsetup/observe_test.go`

**Interfaces:**
- Consumes: Task 6, 7.
- Produces:
  ```go
  func Observe(ctx context.Context, env Env, opts ObserveOptions) State
  func PingServer(ctx context.Context, env Env, ep modelping.Endpoint) ServerState // the Task 6 body of modelsetup.pingServer
  func ExpandIn(path, home string) string // plan.go:713-718 over the moved expandHome (needed here by Observe's callers)
  ```
  `modelmode.Observe` body, in this order (from observe.go): config, `URLHadCredentials`, `Problems`, `Resolution`, `EnvOverride` (:257-299); `home, herr := env.Host.Home()`, `st.Host.Home`, `st.Host.ConfigHome = configHome(getenv("XDG_CONFIG_HOME"), home, herr)` (:304-309 without UID, PATH, binary); token choice (:347-365) with its fourth case reading `opts.Local.TokenFile` in place of `st.Agent.TokenFile`; `TokenFiles` (:367-389) with `opts.Local.TokenFile` third (after the default local and remote paths, before `opts.TokenPaths`) **in every mode, as today** (:373 is not mode-gated); server (:483-523) with the local url default `opts.Local.Listen`, else `DefaultListen`, only when `Resolution.Mode == ModeLocal` (as :489). `readToken`, `expandHome`, `configHome`, `envOverrideNames` move unexported; `readToken` uses `DanglingLink(env.FS, …)`.
  `modelsetup.Observe` order: platform (:247-255); `home, herr := env.Host.Home()`; `st.Tools.UID`, `st.Tools.Path = getenv("PATH")`, `st.Tools.Binary, st.Tools.BinaryErr = findModelBinary(env)`; agent plist and `Launchd.Print` (:311-338, `danglingLink(env, …)` → `modelmode.DanglingLink(env.FS, …)`); `o := opts.ObserveOptions; o.Local = modelmode.LocalFallback{TokenFile: st.Agent.TokenFile, Listen: st.Agent.Listen}; st.State = modelmode.Observe(ctx, env.Mode(), o)`; pair (:391-401); LiteLLM plist, job, install, listener (:403-481); `CheckLiteLLM` in local mode (:525-528). `Host.Home` is now called twice and the ping runs before the listener and LiteLLM probes (no test pins call order across those seams; `TestFakeEnvRecordsCallsInOrderAndAdvancesTheClock` pins only the fake).

- [ ] **Step 1: Write the failing tests.**
  - `modelmode/fakes_test.go` (test-only copy of the session half of `modelsetup/fakes_test.go`): `fakeEnv` with `calls`, `files`, `modTimes`, `modes`, `writeErr`, `dangling`, `configFile`, `updateErr`, `ping`, `pingErr`, `pingErrs`, `pingTokens`, `env`, `home`; `newFakeEnv(t)` with `home: "/Users/user_name"`; `Env()` returning `Env{FS: fakeFS{f}, Config: fakeConfig{f}, Net: fakeNet{f}, Host: fakeHost{f}}`; `rec`, `called`, `mutations`; the FS/Config/Net/Host methods copied with the same record strings (`"Net.Ping %s"`, `"FS.Chmod %s %04o"` …), `fakeNet.Ping` as in Task 6.
  - Move to `modelmode/observe_test.go` (with a copy of `obsConfigured`, `obsTokenPath`, `obsFakeToken`; `Observe(context.Background(), f.Env(), ObserveOptions{…})`; `hiddenURL` → `urlrule.Hidden`; `modelclient.X` error types → `modelping.X`): `TestObserveMissingConfigIsEmptyNotAnError`, `TestObserveLoosenessOfTheTokenFile`, `TestObserveNamesEnvOverrides`, `TestObserveKeepsAnInlineConfigTokenOutOfState`, `TestObserveExpandsATildeInTheConfiguredTokenFile`, `TestObserveTokenChoiceMirrorsTheSession`, `TestObserveRecordsThePingedURLWithoutUserinfo`, `TestObserveStripsUserinfoFromTheConfiguredURL`, `TestObservePingsTheEnteredEndpoint`. Delete `TestObserveClassifiesEachPingFailure` from modelsetup (Task 3's `TestClassifyMapsEachPingError` is its table).
  - New in `modelmode/observe_test.go`:
    - `TestObserveRecordsAClassifiedPingFailure`: `obsConfigured`, `f.pingErr = &modelping.SkewError{Addr: "a", Server: 2, Client: 1}` → `Server.Checked`, `!Server.OK`, `Server.Kind == KindSkew`, `Server.API == 2`, `Server.Message == f.pingErr.Error()`.
    - `TestObserveUsesTheLocalFallbackOnlyInLocalMode`: config `Mode: "local"`, no url, no token_file; `f.files["/a/tok"] = "tok-AGENT\n"` mode 0600; `opts.Local = LocalFallback{TokenFile: "/a/tok", Listen: "127.0.0.1:9911"}` → `f.called("Net.Ping http://127.0.0.1:9911")`, `f.pingTokens == ["tok-AGENT"]`, `Token.Path == "/a/tok"`, `len(TokenFiles) == 2` (`…/wt-model/server.token`, `…/agent-wt/remote-model-server.token`) and no `TokenFiles` entry is `/a/tok` (it is the chosen `Token`, which seeds `seen` and is skipped, observe.go:368); same with `Mode: "remote"` → no `Net.Ping`, `Token.Path == ""`, and `/a/tok` still listed third in `TokenFiles`.
    - `TestObserveWithoutAFallbackPingsTheDefaultLocalURL`: config `Mode: "local"`, `TokenFile: obsTokenPath`, no url, zero `Local` → `f.called("Net.Ping http://127.0.0.1:8787")`.
  - New in `modelsetup/observe_test.go`: `TestObservePassesTheAgentAsTheLocalFallback`: a managed agent plist (`RenderAgentPlist(AgentSpec{Binary: "/b/wt-model", Listen: "127.0.0.1:9911", TokenFile: "/a/tok", Path: "/bin", Home: "/h"})` at `obsAgentPath`), `/a/tok` 0600 holding `tok-AGENT`, config `Mode: "local"` with no url and no token_file → `f.called("Net.Ping http://127.0.0.1:9911")`, `f.pingTokens == ["tok-AGENT"]`, `st.Agent.Listen == "127.0.0.1:9911"`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode -count=1` — expect `undefined: Observe`.
- [ ] **Step 3: Implement** `modelmode/observe.go` and the new `modelsetup.Observe` shape above; delete the moved helpers from modelsetup (`expandIn` there becomes `modelmode.ExpandIn` at its call sites, plan.go:322,427,591).
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session; the baseline `comm` check prints only `TestObserveClassifiesEachPingFailure`; `go test ./internal/modelsetup -count=1 -run 'Observe|LiteLLM|Pair|AtURL|Dangling|Credential' -v | grep -c '^--- FAIL'` prints 0.
- [ ] **Step 5: Commit** `refactor(modelmode): Observe reads the session half; modelsetup.Observe adds the admin reads around it` — the files above.

### Task 9: Desired and Plan types; modelsetup embeds them

**Files:**
- Modify: `internal/modelmode/plan.go` (add types and methods), `internal/modelsetup/plan.go:54-60` (Desired), `:152-165` (Plan), `:216,278,354,513` (Plan literals), `:622-649` (leaveAgent: `p.Want.RemoveAgent` → `p.Admin.RemoveAgent`), `:676-693,765-770,779-789` (Format, plural, WritesURL: move), `:952` (tighten literal); `internal/modelsetup/apply.go:339` (`p.Want.MasterKey` → `p.Admin.MasterKey`); `internal/configeditor/server_tab.go:300` (`d := modelsetup.Desired{Mode: st.want.Mode}` → `var d modelsetup.Desired; d.Mode = st.want.Mode`); tests: every `Desired{<field>:` literal in `internal/modelsetup/*_test.go` (about 150) and `internal/configeditor/*_test.go`, every `Plan{Want:` / `Plan{Steps:` / `Plan{Notes:` literal, `plan_test.go:328` (`p.Want.Listen` → `p.Admin.Listen`), `:595-603` (`p.Want.MasterKey` → `p.Admin.MasterKey`), `:981-982` (`p.Want.LiteLLM` → `p.Admin.LiteLLM`), `configeditor/server_tab_test.go:1012,1225` (`.Want.MasterKey` → `.Admin.MasterKey`)
- Test: `internal/modelmode/plan_test.go`, `internal/modelsetup/plan_test.go`

**Interfaces:**
- Consumes: Task 5, 7.
- Produces:
  ```go
  // modelmode
  type Desired struct{ Mode Mode; URL, TokenFile string; PastedToken Secret; SaveUnchecked bool }
  type Plan struct{ Want Desired; Steps []Step; Notes []string }
  func (p Plan) Format() string   // plan.go:676-693 (+ plural)
  func (p Plan) WritesURL() bool  // plan.go:782-789
  // modelsetup
  type AdminWant struct{ Listen string; MasterKey Secret; RemoveAgent bool; LiteLLM LiteLLMAction }
  type Desired struct{ modelmode.Desired; AdminWant }
  type Plan struct{ modelmode.Plan; Admin AdminWant; Agent *AgentSpec; LiteLLM *LiteLLMSpec }
  ```
  Behaviour unchanged: `BuildPlan` builds `Plan{Plan: modelmode.Plan{Want: want.Desired}, Admin: want.AdminWant, …}` where it built `Plan{Want: want, …}`; native and remote set `want.Listen = ""` before that, as today; `config()`/`envNote()`/`restartNote()` stay modelsetup methods (they read promoted fields). Test literal form: `Desired{Desired: modelmode.Desired{Mode: …, URL: …, PastedToken: …}, AdminWant: AdminWant{RemoveAgent: true}}` (`AdminWant` left out when empty); `Plan{Plan: modelmode.Plan{Want: modelmode.Desired{…}, Steps: []Step{…}}}`. A promoted field cannot be named in a composite literal, hence the rewrite; a small throwaway `go/ast` program under `$TMPDIR` may do it (never committed).

- [ ] **Step 1: Write the failing tests.**
  - `modelmode/plan_test.go` `TestFormatNamesEveryStepAndNote`: `Plan{Want: Desired{Mode: ModeNative}}.Format() == "nothing to change for mode native\n"`; `Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Detail: "a"}, {Detail: "b"}}, Notes: []string{"n"}}.Format() == "set up mode remote in 2 steps:\n  1. a\n  2. b\nleft alone and notes:\n  - n\n"`; one step reads `"… in 1 step:\n"`. `TestWritesURLNeedsAConfigStepAndAURL`: `{Want{URL: "u"}, Steps{{Kind: StepConfig}}}` → true; `{Want{URL: "u"}, Steps{{Kind: StepToken}}}` → false; `{Want{URL: ""}, Steps{{Kind: StepConfig}}}` → false.
  - `modelsetup/plan_test.go`: the `p.Admin.*` edits in Files (they fail to compile first).
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode ./internal/modelsetup -count=1` — expect `undefined: Desired` (modelmode) and `p.Admin undefined` (modelsetup).
- [ ] **Step 3: Implement** the types and moves above; rewrite the literals. Check: `grep -nE '(^|[^.])Desired\{(Mode|URL|TokenFile|PastedToken|SaveUnchecked|Listen|MasterKey|RemoveAgent|LiteLLM):' internal/modelsetup/*_test.go internal/configeditor/*_test.go` and `grep -nE 'Plan\{(Want|Steps|Notes):' internal/modelsetup/*_test.go internal/configeditor/*_test.go` print nothing.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast (the admin goldens unchanged: `TestFormatMatchesTheGoldens` passes in modelsetup without `UPDATE_GOLDEN`); G-session; the baseline `comm` check.
- [ ] **Step 5: Commit** `refactor(modelsetup): Desired and Plan embed modelmode's; admin wants move to Plan.Admin` — the files above.

### Task 10: `modelmode.BuildPlan` (native, remote, local pointer); modelsetup delegates; goldens split

**Files:**
- Create: `internal/modelmode/testdata/plan_native.golden`, `plan_remote.golden`, `plan_local_pointer.golden`, `plan_native_from_local.golden`
- Modify: `internal/modelmode/plan.go`; `internal/modelsetup/plan.go:185-243` (BuildPlan), `:275-369` (planNative, planRemote: move), `:586-618` (config → `AddConfigStep`), `:667-672` (envNote → `AddEnvNote`), `:695-752` (tokenAt, expandIn, sameEndpoint, serverFailure: move); `internal/modelsetup/apply.go:85` (`serverFailure` → `modelmode.ServerFailure`); `internal/modelsetup/plan_test.go`, `observe_test.go:602-629`, `finalreview_test.go:22-37` (moves below)
- Test: `internal/modelmode/plan_test.go`, `internal/modelsetup/plan_test.go`

**Interfaces:**
- Consumes: Task 5, 7, 8, 9.
- Produces:
  ```go
  func Preflight(st State, want Desired) (Desired, error) // plan.go:188-210: ParseErr, TableErr, ParseMode refusals (texts unchanged); want.Mode = parsed; PastedToken = "" unless remote
  func Postflight(st State, p Plan) error                  // plan.go:236-240: RestErr refusal when p has a StepConfig
  type PlanHooks struct{ LeavingLocal func(p *Plan) }
  func BuildPlan(st State, want Desired) (Plan, error)     // = BuildPlanWith(st, want, PlanHooks{})
  func BuildPlanWith(st State, want Desired, h PlanHooks) (Plan, error) // Preflight; native | remote | local pointer; Postflight
  func (p *Plan) AddConfigStep(st State) // plan.go:588-618
  func (p *Plan) AddEnvNote(st State)    // plan.go:668-672
  func ServerFailure(s ServerState) string // plan.go:734-752
  func TokenAt(st State, path string) (TokenState, bool) // plan.go:696-709
  ```
  Native (plan.go:276-284): `want.URL, want.TokenFile = "", ""`; `p := Plan{Want: want}`; leaving local; `AddConfigStep`; `AddEnvNote`. Remote (:288-369, `st.Host.ConfigHome`/`Home` as today, `sameEndpoint` unexported here): token step; leaving local; `AddConfigStep`; check notes; `AddEnvNote`. "Leaving local" is `h.LeavingLocal(&p)` when set, else, when `st.Resolution.Mode == ModeLocal`, the note `` "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`" ``. Local pointer (new): `t := st.Config.Table`; `m, ok := ParseMode(t.Mode); storedLocal := ok && m == ModeLocal` (the stored table's mode, not the resolution: an env override does not count); when `storedLocal`, a stored url holding an `@` (`urlrule.HasUserinfo(t.URL)`, true for the `Hidden` placeholder Observe stores) refuses alone with `CredentialsReason("the stored url")`, `want.URL = t.URL`, else `"http://" + DefaultListen`, and `want.TokenFile = ExpandIn(t.TokenFile, st.Host.Home)`, else `DefaultLocalTokenPath(st.Host.ConfigHome)`; when not `storedLocal` (stored mode remote, native or unset) the stored url and token file are ignored (a remote url must never become local's url; an `@` in them is not refused, it is overwritten) and `want.URL = "http://" + DefaultListen`, `want.TokenFile = DefaultLocalTokenPath(st.Host.ConfigHome)`; and when that is needed with `ConfigHome == ""` refuse with `Reason("the config directory is not known, so the local token file has no default path", "set HOME or XDG_CONFIG_HOME")`; the entered `want.URL`/`TokenFile` are ignored; `AddConfigStep` (an unchanged table gives its existing "keep [model-server] …" note); note `` "run `wt-model setup` to set up wt-model on this machine" ``; `AddEnvNote`.
  modelsetup: `BuildPlan(st State, want Desired)`: `d, err := modelmode.Preflight(st.State, want.Desired)`; `want.Desired = d`; clear `MasterKey`, `LiteLLM` unless local (:212-215); local → `planTightenLiteLLM` (returned directly, as :220-222 today: no Postflight, no restart note) or `planLocal` (which calls `p.AddConfigStep(st.State)`, `p.AddEnvNote(st.State)`, `modelmode.TokenAt(st.State, …)`); native/remote → `mp, err := modelmode.BuildPlanWith(st.State, want.Desired, modelmode.PlanHooks{LeavingLocal: func(p *modelmode.Plan) { /* leaveAgent then keepLocalSetup, over st and want.AdminWant */ }})` then `p := Plan{Plan: mp, Admin: want.AdminWant}` with `p.Admin.Listen = ""`; then `p.restartNote()`. `modelmode.Postflight(st.State, p.Plan)` is called by modelsetup **only on its `planLocal` path** (after `planLocal`, as plan.go:236-240 does today; `planTightenLiteLLM` still skips it): `BuildPlanWith` already ran it for native and remote, so modelsetup does not repeat it there. `BuildPlanWith` does repeat `Preflight` on the native/remote path; that is deliberate and idempotent (its input mode is already parsed, its refusals already passed, and `PastedToken` is already cleared unless remote), so it changes no output. `leaveAgent`/`keepLocalSetup` become functions taking `(p *modelmode.Plan, st State, a AdminWant)`. Every existing plan's text is unchanged.

- [ ] **Step 1: Write the failing tests** (in `modelmode/plan_test.go`, with test-only consts `plHome = "/Users/user_name"`, `plConfigHome`, `plCfgPath`, `plLocalTok`, `plRemoteTok` of modelsetup/plan_test.go:16-27 and helpers `kinds`, `sameKinds`, `stepOf`, `mustRefuse`, `mustPlan` (its secret checks for `plPasted` only), `freshState()` = `State{Config: ConfigState{Path: plCfgPath}, TokenFiles: []TokenState{{Path: plLocalTok}, {Path: plRemoteTok}}, Host: HostState{Home: plHome, ConfigHome: plConfigHome}}`, `localState()` = fresh with `Config{Path: plCfgPath, Exists: true, Table{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: plLocalTok}}` and `Resolution{Mode: ModeLocal, Source: SourceStored}`):
  - Move from modelsetup (same names; `BuildPlan` is modelmode's; `Desired{…}` plain): `TestBuildPlanRefusesAnUnknownMode`, `TestBuildPlanRefusesAParseErrorBeforeAnyStep`, `TestBuildPlanRefusesAnUnreadableTableBeforeAnyStep`, `TestBuildPlanRefusesAKeyElsewhereInTheFile` (all loop the three modes; local is now the pointer), `TestBuildPlanRemoteRefusesAFailedCheckUnlessSavedUnchecked`, `TestBuildPlanRemoteReplacesADifferentPastedTokenOnlyWhenTheTextSaysSo`, `TestBuildPlanRemoteCheckOfAnotherURLIsNotAVerification`, `TestBuildPlanRemoteCheckMatchesATypedURLWithCredentials`, `TestBuildPlanRemoteRefusesAURLWithCredentials` (finalreview_test.go:22-37), `TestPlanRefusesAPastedTokenOverADanglingLink`, `TestPlanReplacesAnUnreadableTokenWithAPastedOne` (observe_test.go:602-629, over modelmode's `Observe` and fake), `TestPlanWritesURLIsData` (local → the pointer, true; kept → `localState()` with `Desired{Mode: ModeLocal}`, false), `TestFormatIsDeterministic` (over modelmode's `goldenPlans`).
  - `TestFormatMatchesTheGoldens` (modelmode's copy of modelsetup/plan_test.go:528-558, `UPDATE_GOLDEN=1` regenerates) over `goldenPlans`: `plan_native.golden` = `mustPlan(remoteStored(), Desired{Mode: ModeNative})` where `remoteStored()` is fresh with `Table{Mode: "remote", URL: "https://gpu.example:8787", TokenFile: plRemoteTok}`, `Exists: true`, `Resolution{ModeRemote, SourceStored}`; `plan_remote.golden` = fresh with `TokenFiles[1].Exists = true`, `Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}`; `plan_local_pointer.golden` = `mustPlan(freshState(), Desired{Mode: ModeLocal})`; `plan_native_from_local.golden` = `mustPlan(localState(), Desired{Mode: ModeNative})`. Their exact content (write these files by hand first; the test must then pass without `UPDATE_GOLDEN`):
    - `plan_native.golden`:
      ```
      set up mode native in 1 step:
        1. write [model-server] in /Users/user_name/.config/agent-wt/config.toml: mode = "native"; clear url and token_file
      ```
    - `plan_remote.golden`:
      ```
      set up mode remote in 2 steps:
        1. replace /Users/user_name/.config/agent-wt/remote-model-server.token with the pasted token (mode 0600; the old token there is lost)
        2. create [model-server] in /Users/user_name/.config/agent-wt/config.toml: mode = "remote", url = "https://gpu.example:8787", token_file = "/Users/user_name/.config/agent-wt/remote-model-server.token"
      left alone and notes:
        - the server at https://gpu.example:8787 was not checked
      ```
    - `plan_local_pointer.golden`:
      ```
      set up mode local in 1 step:
        1. create [model-server] in /Users/user_name/.config/agent-wt/config.toml: mode = "local", url = "http://127.0.0.1:8787", token_file = "/Users/user_name/.config/wt-model/server.token"
      left alone and notes:
        - run `wt-model setup` to set up wt-model on this machine
      ```
    - `plan_native_from_local.golden`:
      ```
      set up mode native in 1 step:
        1. write [model-server] in /Users/user_name/.config/agent-wt/config.toml: mode = "native"; clear url and token_file
      left alone and notes:
        - wt-model's LaunchAgent is untouched; remove it with `wt-model setup`
      ```
  - New: `TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal` (table-driven; every row `Desired{Mode: ModeLocal, URL: "http://ignored:1", TokenFile: "/ignored"}`, so a typed url or token file is never used; each row asserts `p.Want.Mode == ModeLocal`, the two wants and, except row (a), exactly one `StepConfig`; the comment says a remote url must never become local's url): (a) "stored local": fresh with `Config{Exists: true, Table{Mode: "local", URL: "http://127.0.0.1:9999", TokenFile: "/x/t"}}`, `Resolution{ModeLocal, SourceStored}` → no `StepConfig` (the table is unchanged: the "keep [model-server]" note), `p.Want.URL == "http://127.0.0.1:9999"`, `p.Want.TokenFile == "/x/t"`; (b) "stored local, no url" (Review Focus #4): `Table{Mode: "local", TokenFile: "/x/t"}` → one `StepConfig`, `p.Want.URL == "http://" + DefaultListen`, `p.Want.TokenFile == "/x/t"` (only the empty field is filled); (c) "stored local, `~` token": `Table{Mode: "local", TokenFile: "~/t"}` → `p.Want.URL == "http://" + DefaultListen`, `p.Want.TokenFile == "/Users/user_name/t"`; (d) "stored remote": `remoteStored()` with `Table.URL = "https://gpu.example:8787"`, `Table.TokenFile = "/y/t"` → one `StepConfig`, `p.Want.URL == "http://" + DefaultListen`, `p.Want.TokenFile == DefaultLocalTokenPath(plConfigHome)`, and `p.Format()` contains neither `"gpu.example"` nor `"/y/t"`; (e) "stored remote, at-url": `remoteStored()` with `Table.URL = urlrule.Hidden`, `URLHadCredentials: true` → plans (no refusal), wants as (d); (f) "stored native": `Table{Mode: "native"}`, `Resolution{ModeNative, SourceStored}` → wants as (d); (g) "mode unset": `freshState()` with `Table{TokenFile: "/z/t"}` → wants as (d) (`"/z/t"` dropped). Each "as (d)" row also asserts `p.Format()` contains `` `url = "http://` + DefaultListen + `"` `` (built from the constant, never a literal of the test's own). ``TestBuildPlanLocalPointerIsANoteWhenAlreadyLocal`: `localState()`, `Desired{Mode: ModeLocal}` → no step; notes `["keep [model-server] in /Users/user_name/.config/agent-wt/config.toml (already mode = \"local\")", "run `wt-model setup` to set up wt-model on this machine"]`. `TestBuildPlanLocalPointerRefusesAStoredAtURL`: fresh with `Table{Mode: "local", URL: urlrule.Hidden}`, `URLHadCredentials: true` → `*RefusalError` with exactly one reason `CredentialsReason("the stored url")` (only a kept url is refused; row (e) above pins that a remote at-url is overwritten). `TestBuildPlanLocalPointerNeedsAConfigHomeForTheDefaultToken`: fresh with `Host.ConfigHome = ""` → refusal containing `"set HOME or XDG_CONFIG_HOME"`; the same with `Table{Mode: "local", TokenFile: "/x/t"}` plans; the same with `Table{Mode: "remote", TokenFile: "/x/t"}` still refuses (that token file is not kept). `TestBuildPlanWithRunsTheHookInPlaceOfTheNote`: `localState()`, `BuildPlanWith(…, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true}, PlanHooks{LeavingLocal: func(p *Plan) { p.Notes = append(p.Notes, "HOOK"); p.Steps = append(p.Steps, Step{Kind: "hook"}) }})` → kinds `[token, hook, config]`, notes contain `"HOOK"` and not `"LaunchAgent is untouched"`.
  - modelsetup keeps (unchanged) every local, LiteLLM, agent and golden test, plus `TestBuildPlanNativeIsOnlyTheConfigStep`, `TestBuildPlanNativeOffersToRemoveAManagedAgent`, `TestBuildPlanLeavingLocalNeverRemovesTheTokenOrThePair`, `TestBuildPlanKeepsEachSecretToItsMode`, `TestObserveAndPlanFlagACredentialedURLThatWillBeRewritten`, `TestBuildPlanRefusesAConfigFileItCannotWrite`, `TestPlansSayARestartCutsRequestsInFlight` — they now run through the delegation and the hook, and `plan_native.golden`/`plan_remote.golden` in `internal/modelsetup/testdata` are the admin goldens (LaunchAgent left installed, bootout, remove-plist, pair kept), byte-identical.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode -count=1` — expect `undefined: BuildPlan`, `undefined: PlanHooks`.
- [ ] **Step 3: Implement** `modelmode/plan.go` additions and the modelsetup delegation above; move the listed tests (delete them from modelsetup).
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; `git diff --stat internal/modelsetup/testdata` is empty; G-session; the baseline `comm` check (moved names now pass in modelmode).
- [ ] **Step 5: Commit** `refactor(modelmode): native, remote and local-pointer plans; modelsetup plans local and hooks the agent notes` — the files above.

### Task 11: `modelmode.Apply` and `ApplyStep`; modelsetup delegates the token and config steps

**Files:**
- Modify: `internal/modelmode/apply.go`; `internal/modelsetup/apply.go:96-117` (Apply loop: `failed`, `applyStep`), `:160-272` (applyStep), `:274-318` (applyToken, sameToken: the pasted/tighten half moves), `:384-413` (waitReady: `modelmode.PingServer(ctx, env.Mode(), ep)`, `ep := modelping.Endpoint{…}`); `internal/modelsetup/apply_test.go:363-412`, `finalreview_test.go:52-64` (moves)
- Test: `internal/modelmode/apply_test.go` (new), `internal/modelsetup/apply_test.go`

**Interfaces:**
- Consumes: Task 5, 6, 9.
- Produces:
  ```go
  func Apply(ctx context.Context, env Env, p Plan, progress func(string)) Result // apply.go:96-117 without Recovery; progress "step %d/%d: <label>", label = StepLabel or "unknown step"; Err = fmt.Errorf("%s: %w", label, err)
  func ApplyStep(ctx context.Context, env Env, p Plan, s Step, r *Result) (handled bool, err error)
  ```
  `ApplyStep`: `StepConfig` → apply.go:248-254 (the `@` backstop text unchanged, then `env.Config.Update`); `StepToken` → `s.Path == ""` refusal (:276-278), `ActionTightenToken` (:286-287), `ActionWritePastedToken`/`ActionReplacePastedToken` (:288-299, `sameToken` moves), any other token action → `(true, fmt.Errorf("token action %q is not one Apply knows", s.Action))` (today's text, :301); any other kind → `(false, nil)`. `modelmode.Apply`: not handled → `fmt.Errorf("step kind %q is not one Apply knows; nothing of it was done", s.Kind)` (:271), so the session binary can never run an admin step.
  modelsetup `applyStep(ctx, env, p, s, r, run)`: `StepToken` with `ActionGenerateToken` (:280-285, it needs `Host.RandRead`) handled here; its admin kinds as today; then `if handled, err := modelmode.ApplyStep(ctx, env.Mode(), p.Plan, s, r); handled { return err }`; then the unknown-kind refusal (:271). The step order, progress lines, Recovery and every message are unchanged.

- [ ] **Step 1: Write the failing tests** (`modelmode/apply_test.go`, helpers `apEnv` (fake with `configFile.Path = plCfgPath`), `apply`, `mutations`, `noSecrets` copied from modelsetup/apply_test.go:36-101):
  - Move: `TestApplyRemoteWritesThePastedTokenBeforeTheConfig` (apply_test.go:363-412, over a modelmode remote plan), `TestApplyConfigRefusesAURLWithCredentials` (finalreview_test.go:52-64).
  - Twin (modelsetup keeps its local-plan version): `TestApplyTightensALooseTokenFile` over `Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Kind: StepToken, Path: plRemoteTok, Action: ActionTightenToken}}}` → mutations exactly `["FS.Chmod " + plRemoteTok + " 0600"]`, content kept.
  - New `TestApplyRefusesEveryAdminStep`: for each kind in `"pair", "plist", "bootout", "bootstrap", "kickstart", "wait", "remove-plist", "litellm-plist", "backup", "master-key", "chmod", "kickstart-litellm"` followed by a `StepConfig`: `r.Failed.Kind == kind`, `r.Err.Error()` contains `"is not one Apply knows; nothing of it was done"`, `!f.called("Config.Update")`, no `FS.WriteAtomic`/`FS.Chmod`; and `{Kind: StepToken, Path: "/t", Action: "generate-token"}` → `r.Err` contains `` `token action "generate-token" is not one Apply knows` ``, no write.
  - New `TestApplyWritesConfigWithTheWantedFields`: `Plan{Want: Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", TokenFile: plRemoteTok}, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}` → the fake's stored table has those three values, progress `["step 1/1: write [model-server]"]`, `r.Recovery == ""`.
- [ ] **Step 2: Run it to see it fail:** `cd wt && go test ./internal/modelmode -count=1` — expect `undefined: Apply`.
- [ ] **Step 3: Implement** as above.
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; `go test ./internal/modelsetup -count=1 -run 'Apply|Control|NoPlanOrResult' -v | grep -c '^--- FAIL'` prints 0 (in particular `TestApplyFailsAnUnknownStepKind`, `TestApplyIsIdempotent`, `TestApplyRunsStepsInOrderAndWritesConfigLast`, `TestApplyWritesTheTokenFile0600AndKeepsAnExistingOne`); G-session; the baseline `comm` check.
- [ ] **Step 5: Commit** `refactor(modelmode): Apply runs only the token and config steps; modelsetup delegates them` — the files above.

### Task 12: the url-rule tests split; package docs; PR 1c gate

**Files:**
- Modify: `internal/modelsetup/urlcreds_test.go` (split), `internal/modelmode/urlcreds_test.go` (receives), wt `CLAUDE.md` (:102 and :111 keep their names; :307 add `modelmode` to the session library list), `docs/internals/packages.md:75` (the `modelsetup` row: now "extends `internal/modelmode`: embeds its `State`/`Desired`/`Plan`, widens its seams, adds the local, LaunchAgent, pair and LiteLLM parts; `Observe`, `BuildPlan` and `Apply` delegate the session half"), new `internal/modelmode/` row (files `mode.go`, `secret.go`, `state.go`, `env.go`, `observe.go`, `plan.go`, `apply.go`, `urlcreds.go`, `paths.go`; "session-safe: no launchd, LiteLLM or registry code; imports `config`, `modelapi`, `modelping`, `urlrule`"), `:93` (rule: "`modelmode` is session side and must stay off `modelclient`; `modelsetup` extends it"), `wt/Makefile:89` (`single-p`: add `./internal/modelmode` after `./internal/modelsetup`; its `Observe` runs the ping in a goroutine, as modelsetup's did)
- Test: `internal/modelmode/urlcreds_test.go`, `internal/modelsetup/urlcreds_test.go`

**Interfaces:**
- Consumes: Task 8 to 11.
- Produces: no production code.

- [ ] **Step 1: Write the tests.** Copy into `modelmode/urlcreds_test.go` the test-only lists and helpers `credentialedURLs`, `bypassURLs`, `atElsewhereURLs`, `everyCredentialedURL`, `plainURLs`, `hostileAtURLs`, `fixedURLTexts`, `credTokens`, `leaks`, `wantNotCheckedAtURL`, `neverPingedAtURLs`, `atURLObserveCases`, and `modelclientUnreachable` renamed `pingUnreachable` returning `&modelping.UnreachableError{…}` (modelmode's tests cannot import modelclient: it imports modelmode). Move there, names kept: `TestHasUserinfoIsAnyAt` (over `urlrule.HasUserinfo`), `TestBuildPlanRemoteRefusesASchemelessURLWithCredentials`, `TestShownURLHidesTheWholeURL`, `TestRedactCheckHidesAnyQuote`, `TestApplyConfigRefusesASchemelessURLWithCredentials`, `TestHiddenURLIsNeverWrittenToConfig`, `TestPlainURLsPassUntouched`, `TestObserveCheckMessageDropsTheTypedCredentials`, `TestObserveNeverPingsAnAtURL`, `TestObserveStillPingsAPlainURL`. Split `TestNoPieceOfAnAtURLReachesAnyOutput`: modelmode's copy (same name, same assertions, `len(hostileAtURLs) < 60` floor) runs `atURLSurfaces` with the surfaces "BuildPlan refusal", "Apply error", "Apply lines", "Apply notes", "stored: Server.URL", "stored: Server.Message", "stored: Config.Table.URL", "stored: Problems", "Format remote", "Format native", "Format local" (the pointer: a refusal, recorded as "local refusal"), "prefill refusal", "no token: Server.Message", "entered: Server.URL", "entered: Server.Message", "env: Server.URL", "env: Server.Message"; modelsetup's copy keeps only "Apply recovery" (modelsetup.Apply of a config step whose url holds the `@`) and "Format local" (modelsetup.BuildPlan local over a modelsetup.Observe of the stored url), and keeps the lists and helpers those two use (`hostileAtURLs`, `fixedURLTexts`, `credTokens`, `modelclientUnreachable`); delete from modelsetup the helpers no remaining modelsetup test uses (`go vet` does not flag unused test vars: check each with `grep -n '<name>' internal/modelsetup/*_test.go`). `pairurl_test.go` stays as is.
- [ ] **Step 2: Run them:** `cd wt && go test ./internal/modelmode ./internal/modelsetup -count=1 -run 'AtURL|URL|Userinfo|Redact|Credential' -v | grep -E '^(--- FAIL|ok|FAIL)'` — expect two `ok` (a moved test is a copy of a passing one; a failure here means a surface lost its url, fix the copy).
- [ ] **Step 3: Docs and the Makefile** as listed in Files; `docs/internals/packages.md` only names what this PR built (no editor or command text yet).
- [ ] **Step 4: Run the tests, expect PASS:** G-fast; G-session; the baseline `comm` check prints only `TestObserveClassifiesEachPingFailure`; `go list -deps ./internal/modelmode | grep -c internal/modelclient` prints 0; `make check-links` (repo root); `cd wt && make single-p` (background, exit captured).
- [ ] **Step 5: Commit** `test(modelmode): split the url-rule property tests between modelmode and modelsetup` — the files above, `wt/Makefile` included.
- [ ] **PR 1c gate** (Exit gate), then open PR 1c: title `refactor(wt): modelmode State, Observe, Plan and Apply; modelsetup extends them (phase 4 stage 2, PR 1c)`; body: no behaviour change in either binary; the one new behaviour (the local pointer plan and the "untouched" note) is reachable from no binary until PR 3c; the admin goldens are byte-identical and four modelmode goldens were added; the observe call-order change (ping before the listener and LiteLLM probes; `Host.Home` read twice); review commit by commit (Task 7 to 12); `Refs` the design.

---

## PR 2: editorkit extraction (no behaviour change)

**Entry:** PR 1c merged (modelmode owns State/Observe/Plan/Apply; `configeditor` compiles against the modelsetup aliases and embeds). Every line number below is from `origin/main` f60ad48a; 1a–1c touch `configeditor/server_*` only through type spellings and test struct literals, so re-locate a cited block by its function name when the number has shifted.
**Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck ./internal/removal -count=1 && go test ./... -count=1` (the last one ~10 min: run it in the background), plus `go test ./cmd/wt-session -run 'TestConfig|TestGate|TestWtConfigOpens' -count=1`.
**Shape:** a new leaf `internal/editorkit` (imports `themes`, `tuilayout`, bubbletea, bubbles/textinput, lipgloss, stdlib only); `configeditor` calls it and keeps no copy of what moved. No screen changes: every existing `configeditor` test passes unchanged except for the renames listed per task.

Rules for every task: the moved code is copied byte for byte, only renamed (exported) and with the package variable it read turned into a parameter where the signature says so; its doc comment moves with it (reworded only where it named configeditor). After deleting a configeditor copy, drop the imports it leaves unused (`regexp` in `server_view.go`, `os` and `unicode/utf8` in `models_tab.go`, `textinput` in `agent_form.go` if unused: `go build` names them). Every new `Test*` has a top-level `//` comment (what + why). `editorkit` needs no `TestMain`: it reads no file, runs no git and calls nothing in `config` (it links `config` only through `themes`).

---

### Task 13: editorkit package with Scrub and the path helpers; ratchet; `Options.UserHome`

**Files:**
- Create: `wt/internal/editorkit/scrub.go` (package doc comment + `Scrub`, `userinfoRE`, `keyRE`, from `configeditor/server_view.go:106-126`)
- Create: `wt/internal/editorkit/paths.go` (`TildePath` from `models_tab.go:158-170`, `HomePath` from `server_view.go:128-145`, `BreakPath` from `server_view.go:493-526`)
- Test: `wt/internal/editorkit/scrub_test.go`, `wt/internal/editorkit/paths_test.go`
- Modify: `wt/internal/configeditor/server_view.go` (delete :106-145 and :493-526; `tokenLine` :180, `serverStatusLines` :356, `serverStatusGroups` :541, `serverBody` :562-590 thread `home`)
- Modify: `wt/internal/configeditor/server_litellm.go:210` (`plistModeLines`), :232 (`newResultScreen` calls `editorkit.Scrub`)
- Modify: `wt/internal/configeditor/server_tab.go` (every `scrub(` → `editorkit.Scrub(`)
- Modify: `wt/internal/configeditor/models_tab.go:154-176` (delete `userHome` and `tildePath`; `weightsLines` takes `home`), :385, :736, :758 (callers)
- Modify: `wt/internal/configeditor/tabs.go:33-42` (`Options.UserHome`), `wt/internal/configeditor/editor.go:32-75` (`home` field), :458-467 (`Run`)
- Modify tests: `wt/internal/configeditor/testmain_test.go:22` (delete the `userHome` assignment; add `const testUserHome = "/Users/dev"`), `models_tab_test.go:331,410,1176` and `server_tab_test.go:158` (`m.home = testUserHome` right after `newModel`), `models_tab_test.go:554,558,715,716` (`tildePath(x)` → `editorkit.TildePath(x, testUserHome)`), `server_polish_test.go:47` (`homePath(st.Config.Path, "/Users/dev")` → `editorkit.HomePath(st.Config.Path, "/Users/dev", "")`), every `scrub(` in `server_aturl_test.go`, `server_polish_test.go`, `server_tab_test.go`, `server_litellm_test.go` → `editorkit.Scrub(`
- Move tests: `TestBreakPathNeverSplitsASegmentThatFits` (`server_polish_test.go:61-77`) → `editorkit/paths_test.go`; `TestScrubTakesTheHostWithAnAtInTheQuery` (`server_polish_test.go:238-245`), `TestScrubHidesKeysWithoutMangling` (`server_polish_test.go:318-332`), `TestScrubTakesUserinfoThroughTheLastAt` (`server_tab_test.go:1294-1302`) → `editorkit/scrub_test.go` (bodies unchanged except `scrub` → `Scrub`, `breakPath` → `BreakPath`)
- Modify: `wt/internal/depcheck/depcheck_test.go:31-48` and :234-243
- Modify docs: `wt/CLAUDE.md` ("Go module" package list; "Import graph ratchet" paragraph), `wt/docs/internals/packages.md:91-96` ("Package graph rules")

**Interfaces:**
- Consumes: `tuilayout.PadRunes` (unchanged); PR 1a's `TestModeLeavesStayOffTheClient` and its package list.
- Produces (package `editorkit`):
  - `func Scrub(s string) string`
  - `func TildePath(path, home string) string` — `home == "" || home == "/"` → `path`; `path` equal to `home` or under `home + "/"` → `"~" + rest`; else `path`. (Today's `tildePath` also returned `path` on a `userHome` error; the caller now passes `""` for that.)
  - `func HomePath(path, stateHome, userHome string) string` — `stateHome == "" || stateHome == "/"` → `TildePath(path, userHome)`; else today's prefix rule against `stateHome`.
  - `func BreakPath(path string, width int) []string`
- Produces (package `configeditor`):
  - `Options.UserHome func() (string, error)` — nil means `os.UserHomeDir`.
  - `func resolveHome(f func() (string, error)) string` (editor.go) — `f == nil` → `os.UserHomeDir`; an error → `""`.
  - `model.home string`, set once in `Run`: `m.home = resolveHome(o.UserHome)`.
  - `func weightsLines(r modeladmin.Row, home string) []string`; `func tokenLine(st modelsetup.State, home string) serverLine`; `func serverStatusLines(st modelsetup.State, home string) []serverLine`; `func serverStatusGroups(st modelsetup.State, home string, width int, styles map[tone]lipgloss.Style) []statusGroup`; `func plistModeLines(st modelsetup.State, home string) []serverLine`. Inside them `homePath(p, st.Host.Home)` becomes `editorkit.HomePath(p, st.Host.Home, home)`; `serverBody` passes `m.home` and its parse-error line becomes `editorkit.BreakPath(editorkit.HomePath(st.Config.Path, st.Host.Home, m.home), m.width)`; models_tab's `tildePath(r.Path)` becomes `editorkit.TildePath(r.Path, m.home)` (:385) and `weightsLines(row, m.home)` (:736, :758; :736 is inside a `tea.Cmd` closure: read `m.home` into a local before the closure, as `row` is).

- [ ] Step 1: Write the failing tests.
  - Move the four tests listed above into `editorkit/scrub_test.go` and `editorkit/paths_test.go` (package `editorkit`), calling `Scrub`/`BreakPath`; their assertions are unchanged (`Scrub("https://h:1?x=a@b") == "https://b"`; `Scrub("see https://me:p@ss@gpu.example:8787/x and http://a@b/ or mailto me@host") == "see https://gpu.example:8787/x and http://b/ or mailto me@host"`; `BreakPath("~/aaaa/bbbb/cccc/dddd", 12)` joins to `"~/aaaa/bbbb/|cccc/dddd"`, …). Delete them from `configeditor`.
  - `TestTildePathAbbreviatesOnlyUnderHome` (`paths_test.go`), table of `{path, home, want}`: `{"/Users/dev/x", "/Users/dev", "~/x"}`, `{"/Users/dev", "/Users/dev", "~"}`, `{"/Users/devx/y", "/Users/dev", "/Users/devx/y"}`, `{"/a/b", "", "/a/b"}`, `{"/a/b", "/", "/a/b"}`. Why: the home is now a parameter, and a sibling directory sharing the prefix must never be abbreviated.
  - `TestHomePathPrefersTheObservedHome` (`paths_test.go`), table of `{path, stateHome, userHome, want}`: `{"/Users/dev/t", "/Users/dev", "/Users/other", "~/t"}`, `{"/Users/other/t", "", "/Users/other", "~/t"}`, `{"/Users/other/t", "/", "/Users/other", "~/t"}`, `{"/srv/t", "/Users/dev", "/Users/dev", "/srv/t"}`, `{"/Users/other/t", "/Users/dev", "/Users/other", "/Users/other/t"}` (an observed home wins; the user home is only the fallback).
  - `TestResolveHomeFallsBackToNoAbbreviation` (`configeditor/editor_test.go`): `resolveHome(func() (string, error) { return "/Users/dev", nil }) == "/Users/dev"`; `resolveHome(func() (string, error) { return "", errors.New("no home") }) == ""`. Why: a missing home must leave paths written in full, as the package-var seam did.
  - In `wt/internal/depcheck/depcheck_test.go`: append `"internal/editorkit"` to `sessionLibraries` (:45-49) and to the package list `TestModeLeavesStayOffTheClient` iterates (added in PR 1a); replace the single-package `TestAdminScreensStayOffTheSessionScreens` (:234-243) with a loop over a new `var adminScreens = []string{"internal/configeditor", "internal/editorkit"}`, each hit reported as `t.Errorf("%s links %s; share styling through internal/themes or internal/tuilayout instead", pkg, hit)`; update the `adminScreensBanned` comment (:31-34) to say it covers `adminScreens` (the editors and the UI leaf they share).
- [ ] Step 2: Run them to see them fail: `cd wt && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1` — expected: `editorkit` has no non-test Go files / `undefined: Scrub`, `undefined: resolveHome`, and depcheck fails `go list` on `internal/editorkit`.
- [ ] Step 3: Implement: create `scrub.go` and `paths.go` with the signatures above (copies of the configeditor bodies; `TildePath` is `tildePath` with `home` passed instead of `userHome()`); package doc on `scrub.go`: "Package editorkit is the UI plumbing the `wt config` and `wt-model` editors share: scrubbing, path and text fitting, the status and prompt layouts, the result screen, form inputs and the tab bar. It knows no model-server type." Then switch `configeditor` as listed under Files (delete the copies, thread `home`, add `Options.UserHome` with the doc "UserHome is the home directory paths are abbreviated against when the observed state names none; nil means os.UserHomeDir. It is read once, in Run.", `resolveHome`, `m.home`, test edits).
- [ ] Step 4: Run the tests, expect PASS: `cd wt && gofmt -l ./internal && go vet ./internal/editorkit ./internal/configeditor ./internal/depcheck && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1 && go test ./cmd/wt-session -run 'TestConfig|TestGate|TestWtConfigOpens' -count=1` (gofmt prints nothing). If a `configeditor` test still fails on a `/Users/dev/...` path printed in full, it builds its model with `newModel` outside the four sites above: set `m.home = testUserHome` there, nothing else.
- [ ] Step 5: Update docs in the same commit: `wt/CLAUDE.md` adds `editorkit` to the "Go module" package list and, in "Import graph ratchet", adds `editorkit` to the session library packages and says "`configeditor` and `editorkit` off `tui`"; `wt/docs/internals/packages.md:91` adds `editorkit` to the session library list, :94 becomes "`configeditor` and `editorkit` … must not link `tui`", :96 adds "`editorkit` for editor UI plumbing" to the leaves named. Commit: `refactor(editorkit): new UI leaf with Scrub and the path helpers; configeditor reads the home once` — files: `wt/internal/editorkit/{scrub,paths,scrub_test,paths_test}.go`, `wt/internal/configeditor/{server_view,server_litellm,server_tab,models_tab,tabs,editor,testmain_test,models_tab_test,server_tab_test,server_polish_test,server_aturl_test,server_litellm_test,editor_test}.go`, `wt/internal/depcheck/depcheck_test.go`, `wt/CLAUDE.md`, `wt/docs/internals/packages.md`.

---

### Task 14: text fitting — WrapText, Flow, WrapIndented, FitHints

**Files:**
- Create: `wt/internal/editorkit/wrap.go` (`WrapText`, `Flow` from `models_tab.go:146-152`; `WrapIndented` from `server_view.go:724-750`), `wt/internal/editorkit/hints.go` (`FitHints` from `models_tab.go:396-405`)
- Test: `wt/internal/editorkit/wrap_test.go`, `wt/internal/editorkit/hints_test.go`
- Modify: `wt/internal/configeditor/models_tab.go:146-152` (delete `flow`, `wrapText`), :396-405 (delete `fitHints`); every caller in `models_tab.go`, `models_form.go`, `editor.go`, `server_litellm.go`, `server_view.go` → `editorkit.WrapText` / `editorkit.Flow` / `editorkit.FitHints` / `editorkit.WrapIndented`
- Modify tests: `wt/internal/configeditor/models_tab_test.go` (calls of `flow(`, `wrapText(`, `fitHints(` → `editorkit.`)
- Move test: `TestWrapTextWithoutAWidth` (`models_tab_test.go:1514-1528`) → `editorkit/wrap_test.go` (`wrapText` → `WrapText`; assertions unchanged: width 0 and 80 return the text, `WrapText("one two  three", 7) == "one two\nthree"`)

**Interfaces:**
- Consumes: `tuilayout.WrapText`, `tuilayout.Flow`, `tuilayout.Clip`.
- Produces: `func WrapText(s string, width int) string` (= `tuilayout.WrapText`), `func Flow(units []string, sep string, width int) []string` (= `tuilayout.Flow`), `func WrapIndented(text string, width int) string`, `func FitHints(width int, hints []string) string`.

- [ ] Step 1: Write the failing tests: move `TestWrapTextWithoutAWidth`; add
  - `TestWrapIndentedHangsUnderTheStepNumber` (`wrap_test.go`): `WrapIndented("  1. aaa bbb ccc ddd eee fff ggg hhh iii jjj", 24) == "  1. aaa bbb ccc ddd\n     eee fff ggg hhh iii\n     jjj"`; `WrapIndented("  - x", 0) == "  - x"`; `WrapIndented("  1. aaa bbb ccc ddd eee", 20)` equals `WrapText("  1. aaa bbb ccc ddd eee", 20)` (20 − 5 < 16: plain wrap). Why: a plan's steps must stay apart when wrapped, and a narrow terminal must not hang text into nothing.
  - `TestFitHintsPicksTheFullestThatFits` (`hints_test.go`): `FitHints(10, []string{"0123456789ab", "short"}) == "short"`; `FitHints(0, []string{"0123456789ab", "short"}) == "0123456789ab"`; `FitHints(3, []string{"abcdef"}) == tuilayout.Clip("abcdef", 3)`. Why: the hint line is the last line of every editor screen and must never be wider than the terminal.
- [ ] Step 2: Run to see it fail: `cd wt && go test ./internal/editorkit -count=1` — expected `undefined: WrapText` (and `WrapIndented`, `FitHints`).
- [ ] Step 3: Implement `wrap.go` and `hints.go` with the signatures above (bodies copied; `WrapText`/`Flow` are one-line delegations to `tuilayout`, doc: "the launcher's wrapping, under the names both editors use"); switch every configeditor caller and delete the three local copies plus `wrapIndented`.
- [ ] Step 4: Run, expect PASS: `cd wt && gofmt -l ./internal && go vet ./internal/editorkit ./internal/configeditor && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1`.
- [ ] Step 5: Commit: `refactor(editorkit): move the text wrapping and hint fitting out of configeditor` — files: `wt/internal/editorkit/{wrap,hints,wrap_test,hints_test}.go`, `wt/internal/configeditor/{models_tab,models_form,editor,server_litellm,server_view,models_tab_test}.go`.

---

### Task 15: form inputs and the tab bar

**Files:**
- Create: `wt/internal/editorkit/form.go` (`NewTextInput` from `agent_form.go:13-20`; `FormField`, `RenderFormFields` from `form_view.go:10-33`), `wt/internal/editorkit/tabs.go` (`TabBar` from `tabs.go:137-155`)
- Test: `wt/internal/editorkit/form_test.go`, `wt/internal/editorkit/tabs_test.go`
- Delete: `wt/internal/configeditor/form_view.go`
- Modify: `wt/internal/configeditor/agent_form.go:13-20` (delete `newTextInput`) and its `formField{…}` literals; `models_form.go` and `server_tab.go` (`newTextInput(` → `editorkit.NewTextInput(`; `formField{label: …, value: …, focus: …}` → `editorkit.FormField{Label: …, Value: …, Focus: …}`; `renderFormFields(` → `editorkit.RenderFormFields(`)
- Modify: `wt/internal/configeditor/tabs.go:137-155`: `tabBar` keeps its signature and becomes `func tabBar(theme themes.Theme, current Tab) string { return editorkit.TabBar(theme, tabNames, int(current)) }`; `tabNames` stays here.
- Test that stays: `TestTabBarShowsThreeTabs` (`server_tab_test.go:18-29`), unchanged, now exercising `editorkit.TabBar` through the wrapper; `TestNextTabCycles` stays.

**Interfaces:**
- Produces: `func NewTextInput(value, placeholder string) textinput.Model`; `type FormField struct{ Label, Value string; Focus bool }`; `func RenderFormFields(theme themes.Theme, fields []FormField) string`; `func TabBar(theme themes.Theme, names []string, current int) string`.

- [ ] Step 1: Write the failing tests:
  - `TestNewTextInputKeepsValueAndPlaceholder` (`form_test.go`): `in := NewTextInput("v", "p")`; `in.Value() == "v"`, `in.Placeholder == "p"`, `!in.Focused()`. Why: every form opens pre-filled and unfocused until its cursor reaches the field.
  - `TestRenderFormFieldsMarksTheFocusedRow` (`form_test.go`): `ansi.Strip(RenderFormFields(themes.Theme{}, []FormField{{Label: "Name", Value: "a", Focus: true}, {Label: "URL", Value: "b"}})) == "> Name: a\n  URL: b\n"` (`github.com/charmbracelet/x/ansi`). Why: the focused row must read as focused without colour.
  - `TestTabBarBracketsTheCurrentTab` (`tabs_test.go`): `ansi.Strip(TabBar(themes.Theme{}, []string{"A", "B"}, 1)) == " A   [B]"`; `ansi.Strip(TabBar(themes.Theme{}, []string{"A", "B"}, 0)) == "[A]   B "`. Why: the names are now passed in, and the current tab must be bracketed for a terminal without colour.
- [ ] Step 2: Run to see it fail: `cd wt && go test ./internal/editorkit -count=1` — expected `undefined: NewTextInput` (and `FormField`, `RenderFormFields`, `TabBar`).
- [ ] Step 3: Implement `form.go` and `tabs.go` (bodies copied; `TabBar` loops over `names` and compares `i == current`); delete `form_view.go` and `newTextInput`; switch the callers; reduce `tabBar` to the one-line wrapper.
- [ ] Step 4: Run, expect PASS: `cd wt && gofmt -l ./internal && go vet ./internal/editorkit ./internal/configeditor && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1`.
- [ ] Step 5: Commit: `refactor(editorkit): move the form inputs and the tab bar out of configeditor` — files: `wt/internal/editorkit/{form,tabs,form_test,tabs_test}.go`, `wt/internal/configeditor/{agent_form,models_form,server_tab,tabs}.go`, deleted `wt/internal/configeditor/form_view.go`.

---

### Task 16: the status layout — lines, groups, the drop loop, the mode choice, the status block

**Files:**
- Create: `wt/internal/editorkit/status.go`: `Tone`/consts (`server_view.go:88-97`), `LabelW` (:84-86), `Line` (:99-104), `RenderLine` (:405-427), `Styles` (:563-568), `StatusGroup` (:532-537), `Groups` (:539-553 minus the `serverStatusLines` call), `Body` + `FitBody` (:596-663, the drop loop), `ChoiceLines` (:444-458), `StatusBlock` (`editor.go:135-156`, uncached)
- Test: `wt/internal/editorkit/status_test.go`
- Modify: `wt/internal/configeditor/server_view.go`: delete `serverLabelW`, `tone`+consts, `serverLine`, `renderServerLine`, `statusGroup`; every `serverLine{…}` → `editorkit.Line{…}` (positional literals keep their order: Label, Text, Tone; keyed `serverLine{text: why}` at :464, :467 → `editorkit.Line{Text: why}`), `toneX` → `editorkit.ToneX`, `serverLabelW` → `editorkit.LabelW`; `serverStatusGroups(st, home, width, styles)` becomes `editorkit.Groups(serverStatusLines(st, home), width, styles)` at its one caller and is deleted; `serverBody` (:562) builds `editorkit.Body{Head: head, Groups: groups, Choice: radios, Notes: notes, Fields: fieldLines}` and returns `strings.Join(editorkit.FitBody(b, room, statusDropOrder, dim), "\n")`, its styles from `editorkit.Styles(m.theme)`; `modeChoice` (:444-458) uses `editorkit.ChoiceLines("choose", opts, m.width)`; `checkWord` returns `(string, editorkit.Tone)`. `statusDropOrder` stays in configeditor (it names this tab's labels).
- Modify: `wt/internal/configeditor/server_litellm.go` (`plistModeLines` and any `serverLine`/`toneWarn`), `server_tab.go` (`serverLabelW` uses)
- Modify: `wt/internal/configeditor/editor.go:135-156`: `statusBlock` keeps its cache and calls `editorkit.StatusBlock(m.status, m.width)` on a miss.
- Modify tests: `server_aturl_test.go:254,255,258,259` and `server_polish_test.go:264` (`.text` → `.Text`), `server_polish_test.go:146` (`serverLabelW` → `editorkit.LabelW`); any other `serverLine`/`tone` spelling `go vet` reports.

**Interfaces:**
- Consumes: `Flow`, `WrapText` (Task 14); `tuilayout.PadRunes`; `themes.TokenDim`, `themes.TokenWarning`, `themes.ErrorStyle`.
- Produces: `type Tone int`; `const (ToneNone Tone = iota; ToneDim; ToneWarn; ToneBad)`; `const LabelW = 10`; `type Line struct{ Label, Text string; Tone Tone }`; `func RenderLine(l Line, width int) []string`; `func Styles(theme themes.Theme) map[Tone]lipgloss.Style`; `type StatusGroup struct{ Label string; Lines []string }`; `func Groups(lines []Line, width int, styles map[Tone]lipgloss.Style) []StatusGroup`; `type Body struct{ Head []string; Groups []StatusGroup; Choice []string; Notes [][]string; Fields []string }`; `func FitBody(b Body, room int, dropOrder []string, dim lipgloss.Style) []string`; `func ChoiceLines(label string, opts []string, width int) []string`; `func StatusBlock(status string, width int) string`. `Body.Fields` is drawn as given (the caller keeps its leading blank line, as `serverBody` does today).

- [ ] Step 1: Write the failing tests (`status_test.go`):
  - `TestRenderLineHangsUnderTheLabel`: `l := Line{Label: "token", Text: "aaa bbb ccc ddd eee fff ggg"}`; `RenderLine(l, 26)` = `{"token     aaa bbb ccc ddd", "          eee fff ggg"}`; `RenderLine(l, 25)` = `{"token aaa bbb ccc ddd eee", "fff ggg"}` (too narrow for the column: the whole entry flows); `RenderLine(l, 0)` = `{"token     aaa bbb ccc ddd eee fff ggg"}`. Why: no status line may be wider than the terminal, and continuation lines must sit under the text.
  - `TestGroupsFoldsUnlabelledLinesIntoTheRowBefore`: `Groups([]Line{{Label: "mode", Text: "x"}, {Text: "y"}, {Label: "url", Text: "z"}}, 80, map[Tone]lipgloss.Style{ToneNone: lipgloss.NewStyle()})` has 2 groups, labels `"mode"`, `"url"`, `len(g[0].Lines) == 2`. Why: a row and its continuation are dropped whole.
  - `TestFitBodyDropsRowsInOrderBehindOneMarker`: `b := Body{Groups: []StatusGroup{{"a", []string{"A"}}, {"b", []string{"B"}}, {"c", []string{"C"}}}, Choice: []string{"CH"}}`, `plain := lipgloss.NewStyle()`, `order := []string{"b", "a"}`: `FitBody(b, 5, order, plain)` = `{"A", "B", "C", "", "CH"}`; `FitBody(b, 4, order, plain)` = `{"C", "... 2 status rows not shown ...", "", "CH"}`; `FitBody(b, 3, order, plain)` = `{"C", "... 2 status rows not shown ...", "CH"}` (blank lines go last); `FitBody(b, 4, []string{"b"}, plain)` = `{"A", "C", "... 1 status row not shown ...", "", "CH"}` (the order is exhausted; nothing else is dropped by choice). Why: the mode choice must survive every terminal height, and the marker must count what went.
  - `TestChoiceLinesFlowsUnderTheLabel`: `opts := []string{"( ) a", "(x) b"}`; `ChoiceLines("choose", opts, 0)` = `{"choose    ( ) a  (x) b"}`; `ChoiceLines("choose", opts, 80)` = `{"choose    ( ) a  (x) b"}`; `ChoiceLines("choose", opts, 20)` = `{"( ) a  (x) b"}` (too narrow for the label column: today's behaviour, the label is not drawn).
  - `TestStatusBlockWrapsToTheWidth`: `StatusBlock("", 80) == ""`; `StatusBlock("a b", 0) == "a b"`; `lipgloss.Height(StatusBlock("aaa bbb ccc", 4)) == 3`. Why: a status ends with what matters (#209) and must not be cut at the edge.
- [ ] Step 2: Run to see it fail: `cd wt && go test ./internal/editorkit -count=1` — expected `undefined: Line` (and the rest).
- [ ] Step 3: Implement `status.go` with the signatures above; `FitBody` is `serverBody`'s `build`/drop loop/notes trim/`build(false)`/head-middle cut (:598-663) reading `b.Head`, `b.Groups`, `b.Choice`, `b.Notes`, `b.Fields` in place of the locals and returning `out`; `Styles` is the map literal of :563-568. Switch configeditor as listed under Files.
- [ ] Step 4: Run, expect PASS: `cd wt && gofmt -l ./internal && go vet ./internal/editorkit ./internal/configeditor && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1`. The configeditor fit tests (`TestServerTabFitsTheTerminal` `server_tab_test.go:414`, `TestServerTabFitsTheTerminalInEveryPhase` :1070, `TestServerTabFitsTheTerminalWithTheMenu` `server_litellm_test.go:455`, `TestServerTabRecoveryIsOnTheFirstScreenWhenItFits` :380) are the proof that the drop layout is unchanged.
- [ ] Step 5: Commit: `refactor(editorkit): move the status-line layout and its drop order out of configeditor` — files: `wt/internal/editorkit/{status,status_test}.go`, `wt/internal/configeditor/{server_view,server_litellm,server_tab,editor,server_aturl_test,server_polish_test}.go`.

---

### Task 17: prompt views and the result screen

**Files:**
- Create: `wt/internal/editorkit/prompt.go` (`PromptView`, `PlanPromptView` from `server_view.go:695-722`), `wt/internal/editorkit/result.go` (`ResultScreen` from `server_litellm.go:218-323`)
- Test: `wt/internal/editorkit/prompt_test.go`, `wt/internal/editorkit/result_test.go`
- Modify: `wt/internal/configeditor/server_view.go:698-722`: `serverPromptView` keeps choosing `text`/`question` per `st.prompt` and returns `editorkit.PromptView(bar, text, question, m.serverHints(), m.width, m.height, dim)`, or for the plan `editorkit.PlanPromptView(bar, st.planText, "Apply this plan? [y/N]", m.serverHints(), m.width, m.height, dim)`.
- Modify: `wt/internal/configeditor/server_litellm.go:218-323`: delete `serverResultScreen`, `newResultScreen`, `lines`, `resultWindow`; `updateServerResult(key)` becomes: `handled, done := st.result.Key(key, m.width, m.height, lipgloss.Height(tabBar(m.theme, TabServer)), warn, dim); if done { st.result = nil }; return handled`; `serverResultView` becomes `m.server.result.View(bar, m.serverResultHints, m.width, m.height, warn, dim)` (`warn`/`dim` from `m.theme` as `resultWindow` builds them today). `serverResultHints` stays.
- Modify: `wt/internal/configeditor/server_tab.go:136` (the `serverTab.result` field type `*serverResultScreen` → `*editorkit.ResultScreen`), :508 (`st.result = editorkit.NewResultScreen(msg.res.Recovery, msg.res.Notes, st.status)`), :565 (`st.result.Text += "\n" + st.status`)
- Modify test: `wt/internal/configeditor/server_litellm_test.go:609` (`.recovery` → `.Recovery`). Tests that stay and prove no change: `TestServerTabResultScreenEatsCAndR` (`server_litellm_test.go:600`), `TestServerTabScrubsTheNotesAndRecovery` (:413), `TestServerTabPromptsStayVisibleAtEverySize` (`server_polish_test.go:270`).

**Interfaces:**
- Consumes: `Scrub` (Task 13), `WrapText`, `WrapIndented`, `FitHints` (Task 14); `tuilayout.MessageAlone`.
- Produces:
  - `func PromptView(bar, text, question string, hints []string, width, height int, dim lipgloss.Style) string` — body `"\n" + WrapText(text, width) + "\n\n" + WrapText(question, width)`, then `tuilayout.MessageAlone(bar, body, FitHints(width, hints), width, height, dim)`.
  - `func PlanPromptView(bar, planText, question string, hints []string, width, height int, dim lipgloss.Style) string` — the same with `WrapIndented(strings.TrimRight(planText, "\n"), width)` as the text.
  - `type ResultScreen struct{ Recovery string; Notes []string; Text string; Top int }`
  - `func NewResultScreen(recovery string, notes []string, text string) *ResultScreen` — nil when `len(notes) == 0 && recovery == ""`; recovery and each note through `Scrub`; text as given.
  - `func (r *ResultScreen) Lines(width int, warn, dim lipgloss.Style) []string`
  - `func (r *ResultScreen) Key(key string, width, height, barH int, warn, dim lipgloss.Style) (handled, done bool)` — window: `rows = max(height-barH-1, 1)`; when `len(lines) > rows` it scrolls and `rows = max(rows-1, 1)`. `"up"`: `Top = max(Top-1, 0)`; `"down"`: `Top = min(Top+1, max(len(lines)-rows, 0))`; `"esc"`, `"enter"`: `done = true`; `"q"`, `"ctrl+c"`, `"tab"`: `handled = false`; every other key is eaten (`handled = true`).
  - `func (r *ResultScreen) View(bar string, hints func(scrolls bool) []string, width, height int, warn, dim lipgloss.Style) string` — the same window with `barH = lipgloss.Height(bar)`, the marker `"... lines %d-%d of %d; up/down scroll ..."` when it scrolls, then `tuilayout.MessageAlone(bar, …, FitHints(width, hints(scrolls)), width, height, dim)`.

- [ ] Step 1: Write the failing tests (`plain := lipgloss.NewStyle()` throughout; `ansi` = `github.com/charmbracelet/x/ansi`):
  - `TestPromptViewKeepsTheQuestionAboveTheHints` (`prompt_test.go`): `ansi.Strip(PromptView("BAR", "some text", "Apply? [y/N]", []string{"y apply · n cancel"}, 40, 10, plain)) == "BAR\n\nsome text\n\nApply? [y/N]\ny apply · n cancel"`. Why: the question must be the last thing before the hints.
  - `TestPlanPromptViewHangsTheSteps` (`prompt_test.go`): `v := ansi.Strip(PlanPromptView("BAR", "plan:\n  1. aaa bbb ccc ddd eee fff ggg hhh iii jjj\n", "Apply? [y/N]", []string{"y"}, 24, 20, plain))` contains `"\n     eee fff ggg hhh iii\n"` and ends with `"Apply? [y/N]\ny"`.
  - `TestNewResultScreenIsNilWithNothingToShow` (`result_test.go`): `NewResultScreen("", nil, "done") == nil`. Why: an apply with nothing to read by hand goes straight back to the tab.
  - `TestNewResultScreenScrubsRecoveryAndNotes` (`result_test.go`): `r := NewResultScreen("curl https://u:pw@h/x", []string{"key sk-" + strings.Repeat("ab12", 5)}, "text")`; `r.Recovery == "curl https://h/x"`; `r.Notes[0] == "key sk-<hidden>"`; `r.Text == "text"`. Why: nothing an editor shows may hold a credential.
  - `TestResultScreenLinesPutTheRecoveryFirst` (`result_test.go`): `ansi.Strip` of each line of `NewResultScreen("cp a b", []string{"n1"}, "done").Lines(80, plain, plain)` = `{"to put things right by hand:", "cp a b", "", "note: n1", "", "done"}`.
  - `TestResultScreenKeyScrollsAndLeaves` (`result_test.go`): 30 notes `fmt.Sprintf("n%02d", i)`, `r := NewResultScreen("", notes, "")`, call `k := func(s string) (bool, bool) { return r.Key(s, 80, 10, 1, plain, plain) }` (rows 8 → scrolls → 7; max top 23): `k("down")` = `(true, false)` and `r.Top == 1`; `k("up")`, `k("up")` → `r.Top == 0`; 40 × `k("down")` → `r.Top == 23`; `k("c")` = `(true, false)` and `r.Top == 23`; `k("q")`, `k("ctrl+c")`, `k("tab")` each `handled == false`; `k("esc")` and `k("enter")` each `(true, true)`. Why: c and r must not replace the screen while a recovery is unread (today's `TestServerTabResultScreenEatsCAndR` contract, now at the leaf).
  - `TestResultScreenViewCountsTheLinesShown` (`result_test.go`): same 30-note screen; `var asked []bool; hints := func(s bool) []string { asked = append(asked, s); return []string{"esc done"} }`; `v := ansi.Strip(r.View("BAR", hints, 80, 10, plain, plain))`; `v` contains `"... lines 1-7 of 30; up/down scroll ..."`, its last line is `"esc done"`, `lipgloss.Height(v) <= 10`, `asked == []bool{true}`.
- [ ] Step 2: Run to see it fail: `cd wt && go test ./internal/editorkit -count=1` — expected `undefined: PromptView` (and `PlanPromptView`, `NewResultScreen`, `ResultScreen`).
- [ ] Step 3: Implement `prompt.go` and `result.go` with the signatures above (bodies copied from the cited lines; `Key` and `View` share one unexported `window(width, height, barH int, warn, dim lipgloss.Style) (lines []string, rows int, scrolls bool)`); switch configeditor as listed under Files.
- [ ] Step 4: Run, expect PASS: `cd wt && gofmt -l ./internal && go vet ./internal/editorkit ./internal/configeditor && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck -count=1`.
- [ ] Step 5: Commit: `refactor(editorkit): move the confirm prompts and the scrolling result screen out of configeditor` — files: `wt/internal/editorkit/{prompt,result,prompt_test,result_test}.go`, `wt/internal/configeditor/{server_view,server_litellm,server_tab,server_litellm_test}.go`.

---

### Task 18: package docs and the PR gate

**Files:**
- Modify: `wt/docs/internals/packages.md`: a new row after `internal/configeditor/` (:74): `` | `internal/editorkit/` | the UI plumbing `wt config`'s editor (and, from Phase 4 stage 2, both editors) share, with no model-server type: `Scrub` (url userinfo and `sk-` keys out of drawn text), `TildePath`/`HomePath`/`BreakPath`, `WrapText`/`Flow`/`WrapIndented`/`FitHints`, `NewTextInput`/`RenderFormFields`, `TabBar` (names passed in), the status layout (`Line`, `RenderLine`, `Groups`, `FitBody` with a caller's drop order, `ChoiceLines`, `StatusBlock`), `PromptView`/`PlanPromptView` and the scrolling `ResultScreen`; on the session library list and off `tui` (`depcheck`) | ``; the `internal/configeditor/` row (:74) adds "its shared UI plumbing is `internal/editorkit`'s"; the `internal/themes/` row (:82) unchanged.
- Modify: `wt/docs/internals/tui.md` only where it names a moved function by its old name: `grep -rn 'scrub\b\|homePath\|breakPath\|serverPromptView\|wrapIndented\|newResultScreen\|serverResultScreen\|fitHints\|tildePath\|renderFormFields\|newTextInput\|renderServerLine\|serverLabelW' wt/CLAUDE.md wt/docs ../docs/contracts wt/internal --include='*.md' --include='*_test.go'` and rename each hit to the `editorkit.` name (test comments included, e.g. the comment on the moved `TestScrubTakesTheHostWithAnAtInTheQuery`).

**Interfaces:** none new.

- [ ] Step 1: Run the grep above from the repo root; every hit outside `docs/superpowers/` and `docs/archive/` (dated documents keep their wording) is renamed.
- [ ] Step 2: Run `make -C .. check-links` — expected PASS (no link added points nowhere).
- [ ] Step 3: Run the full gate: `cd wt && go build ./... && go vet ./... && make check && go test ./internal/editorkit ./internal/configeditor ./internal/depcheck ./internal/removal -count=1 && go test ./cmd/wt-session -run 'TestConfig|TestGate|TestWtConfigOpens' -count=1`; then `go test ./... -count=1` in the background (about 10 minutes) and read its exit status from the log.
- [ ] Step 4: Commit: `docs(wt): name internal/editorkit in the package map` — files: `wt/docs/internals/packages.md`, plus any file the grep renamed.
- [ ] Step 5: Open the PR: title `refactor(wt): extract internal/editorkit from configeditor (phase 4 stage 2, PR 2)`; body (via `gh pr create --body-file`) says: no behaviour change; what moved (the Task 13–17 list); `configeditor`'s `userHome` package var replaced by `Options.UserHome`, read once in `Run`; ratchet: `editorkit` joins `sessionLibraries` and the mode-leaves list and is held off `tui` with `configeditor`; design `docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md` ("editorkit", staging step 2); `Refs` the phase 4 tracking issue (never close/fix/resolve + `#N`); ends with the attribution line.

---

## PR 3: split configeditor into sessioneditor and modeleditor; wt-model entry points; the first-run gate

The spine (§13) puts PR 3 over the ~1,500-line bar and splits it into three PRs: **3a** (the sync helpers into `internal/litellm`, `config.ValidateSession`, the `modeview` leaf), **3b** (`modeleditor` with the Models tab, `wt-model model`, bare `wt model` becomes a pointer), **3c** (the Setup tab and `wt-model setup`, `sessioneditor` with Agents + Mode, `wt config` and the first-run gate switched over, `configeditor` deleted). This section plans all three. Paths are under `wt/` unless they start with `docs/` (repo root) or `../`. Line numbers are `origin/main` f60ad48a; PRs 1a–2 shift some of them (the function names are the anchor). All commands run from the module root `wt/`.

Conventions used by every task below:
- "the cmd subset" = `go test ./cmd/wt-session -count=1 -run '<regex>'` (the full package takes ~3.5 min and runs only in the PR gate).
- Every new `Test*` gets a top-level `//` comment (what + why); the comment text is not repeated here.
- "green" after every task = `go build ./... && go vet ./... && gofmt -l . | (! grep .) && go test ./internal/depcheck -count=1` plus the task's own tests.

---

### PR 3a: sync helpers into `internal/litellm`, `config.ValidateSession`, the `modeview` leaf

- **Entry:** PR 2 merged on main (`internal/editorkit` exists and `configeditor` draws through it; `internal/modelmode` exists with `State`, `Resolution`, `ServerKind`; `modelsetup.State` embeds `modelmode.State`).
- **Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (run in the background; ~10 min), with `go test ./internal/depcheck ./internal/removal ./internal/litellm ./internal/modeview ./internal/config ./internal/configeditor -count=1` named in the PR body.
- **Size:** ~1,300 changed lines (spine §13). PR body: `Refs #<phase-4 issue>`.

### Task 19: `litellm/syncinputs.go` — the sync inputs leave `cmd/wt-session`

**Files:**
- Create: `internal/litellm/syncinputs.go`, `internal/litellm/syncinputs_test.go`, `internal/litellm/synchelpers_test.go`
- Modify: `cmd/wt-session/litellm.go:132-171` (syncInputs, desiredRouteIDs), `:173-204` (runLitellmSync's first line), `:198-555` (the const and the helpers deleted), `cmd/wt-session/litellm_test.go`, `cmd/wt-session/litellm_dup_test.go`, `cmd/wt-session/loading_test.go:38`, `cmd/wt-session/commands_config.go:46` (unchanged call `desiredRouteIDs`)

**Interfaces:**
- Consumes: `localmodels.Snapshot`, `config.Config`, `litellm.Options`, `litellm.DesiredIDs` (all existing).
- Produces (spine §9):
  ```go
  // internal/litellm/syncinputs.go
  type Probe func(*config.Config) localmodels.Snapshot
  func SyncInputs(cfg *config.Config, probe Probe) ([]config.Model, Options, []string)
  func DesiredRouteIDsFor(probe Probe) func(context.Context) ([]string, error)
  const noProviderEntryReason = "the registry has models for it but no provider entry" // moved verbatim, litellm.go:198
  // unexported, moved as is from cmd/wt-session/litellm.go:198-555 (the const at :198 included):
  // missingProviderWarnings, syncUntouchedAndWarnings, routedIDSet, desiredLocalIDs,
  // desiredLocalEntries, desiredLocalModels, registryRowOf, untrustedFamilies, gapFamily, gapReason
  ```
  `cmd/wt-session` keeps `func desiredRouteIDs(ctx context.Context) ([]string, error) { return litellm.DesiredRouteIDsFor(probeInventory)(ctx) }` (the seam is read at call time, so a stubbed `probeInventory` is honoured).

- [ ] **Step 1: Write the failing tests.** Move, by cut and paste into `internal/litellm/syncinputs_test.go` (package `litellm`), the tests that call only the moved helpers: `TestDesiredLocalIDsOllamaFollowsPulled`, `TestDesiredLocalIDsPulledNeedsTrustedProbe`, `TestUntrustedFamilies`, `TestMissingProviderWarningsOrderAndTheLineAlreadySaid`, `TestSyncRoutesMlxLMServerByProbe`, `TestSyncRoutesMlxLMServerUnattributableAnswerLeavesRoutes`, `TestSyncProbeWarningCarriesTheProbeError`, `TestSyncProbeWarningIgnoresHandWrittenRows` (from `litellm_test.go`) and `TestSyncDownWarningIgnoresADuplicatedID` (from `litellm_dup_test.go`). Bodies and asserted strings unchanged. Copy the cmd test helpers they use into `internal/litellm/synchelpers_test.go`: `litellmTestConfig` (`litellm_test.go:48`) and the const `downProbeWarn` (`litellm_test.go:955`, used by `TestSyncDownWarningIgnoresADuplicatedID`). (`cfgFor` and `cloud` are closures inside the moved tests — `litellm_test.go:1221`, `litellm_dup_test.go:249` — and move with their bodies.) If a remaining `cmd/wt-session` test still uses `downProbeWarn` (`grep -n downProbeWarn cmd/wt-session/*_test.go`), keep its declaration there too. Add one new test there:
  ```go
  // TestSyncInputsRecheckIsTheDesiredLocalSet ... (what + why comment)
  func TestSyncInputsRecheckIsTheDesiredLocalSet(t *testing.T) {
  	calls := 0
  	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
  		Entries: []localmodels.Entry{{ModelID: "ollama/q", ProviderID: "ollama", Running: true}}}
  	probe := func(*config.Config) localmodels.Snapshot { calls++; return snap }
  	desired, o, _ := SyncInputs(&config.Config{}, probe)
  	// one probe for the inputs, one more only when Recheck runs
  	if calls != 1 { t.Fatalf("probes = %d, want 1 before Recheck", calls) }
  	if got := o.Recheck(); !slices.Equal(got, []string{"ollama/q"}) || calls != 2 { t.Fatalf("Recheck = %v after %d probes, want [ollama/q] after 2", got, calls) }
  	if len(desired) != 1 || desired[0].ID != "ollama/q" { t.Fatalf("desired = %+v, want the running ollama/q", desired) }
  }
  ```
  (Use the Entry fields `desiredLocalIDs`' existing test `TestDesiredLocalIDsOllamaFollowsPulled` builds; copy its literal if the field names differ.)
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/litellm -count=1 -run 'TestSyncInputs|TestDesiredLocalIDs|TestUntrustedFamilies|TestMissingProviderWarnings|TestSyncRoutesMlxLMServer|TestSyncProbeWarning|TestSyncDownWarning'` → build failure `undefined: SyncInputs` / `undefined: desiredLocalIDs`.
- [ ] **Step 3: Implement.** `git mv` is not possible for part of a file: cut `cmd/wt-session/litellm.go:132-171` and `:198-555` into `internal/litellm/syncinputs.go`; drop the `litellm.` qualifiers; `syncInputs(cfg)` becomes `SyncInputs(cfg, probe)` with every `probeInventory(cfg)` replaced by `probe(cfg)` (two sites: the snapshot and `Recheck`); `desiredRouteIDs` becomes `DesiredRouteIDsFor(probe)` returning the closure that loads `config.Load()` and calls `DesiredIDs`. In `cmd/wt-session/litellm.go`: `runLitellmSync` calls `litellm.SyncInputs(cfg, probeInventory)`; keep the `desiredRouteIDs` one-line wrapper (above). The mixed tests that stay in `cmd/wt-session` (they also drive `runLitellmSync`) replace the helper calls by two test helpers added to `cmd/wt-session/litellm_test.go`:
  ```go
  func constProbe(s localmodels.Snapshot) litellm.Probe
  func recheckIDs(cfg *config.Config, snap localmodels.Snapshot) []string   // _, o, _ := litellm.SyncInputs(cfg, constProbe(snap)); return o.Recheck()
  func desiredModels(cfg *config.Config, snap localmodels.Snapshot) []config.Model // first return of SyncInputs
  ```
  Sites (the only helper calls left in tests that stay; :1163-1291 are inside tests Step 1 moves): `litellm_test.go:1369,1372` (`TestLitellmSyncDropsDiscoveredEntryCollidingWithRegistryID`), `litellm_dup_test.go:115` (`TestDesiredLocalModelsPairADuplicatedIDByProvider`: `desiredModels(hostCfgOf(a), snap)`), `loading_test.go:38` (`TestALoadingModelKeepsItsRoute`: `recheckIDs(cfg, probeInventory(cfg))`). Confirm with `grep -n 'desiredLocalIDs\|desiredLocalModels\|syncInputs(' cmd/wt-session/*_test.go` → only those lines before the edit, none after. Comments in `cmd/wt-session` and `docs/internals/litellm-routes.md` that name `syncInputs`/`desiredLocalIDs` as cmd functions are updated to `litellm.SyncInputs` (`grep -rn "syncInputs\|desiredLocalIDs\|missingProviderWarnings" docs CLAUDE.md cmd internal`).
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/litellm -count=1` and the cmd subset `go test ./cmd/wt-session -count=1 -run 'Litellm|Sync|Desired|Loading|Untrusted|Gap'`; `go vet ./... && gofmt -l .`
- [ ] **Step 5: Commit.** `refactor(litellm): move the route-sync inputs out of cmd/wt-session into internal/litellm` — files: `internal/litellm/syncinputs.go`, `internal/litellm/syncinputs_test.go`, `internal/litellm/synchelpers_test.go`, `cmd/wt-session/litellm.go`, `cmd/wt-session/litellm_test.go`, `cmd/wt-session/litellm_dup_test.go`, `cmd/wt-session/loading_test.go`, docs touched by the grep.

### Task 20: `litellm/report.go` — the printers and the after-write sync

**Files:**
- Create: `internal/litellm/report.go`, `internal/litellm/report_test.go`
- Modify: `cmd/wt-session/litellm.go:23-130` (types and printers deleted), `:173-198` (runLitellmSync), `cmd/wt-session/model.go:22-53` (syncRoutesAfterWrite seam: `realSyncRoutesAfterWrite`'s body moves out, the function and the var stay), `cmd/wt-session/model_write.go:240-253` (syncAndWarn), `cmd/wt-session/litellm_test.go` (type names), `cmd/wt-session/litellm_dup_test.go:216`

**Interfaces:**
- Consumes: `SyncInputs`, `Probe` (Task 19).
- Produces (spine §9; JSON tags unchanged, `docs/contracts/litellm-cli.sample.json` unchanged):
  ```go
  type OutcomeJSON struct{…}   // was litellmOutcomeJSON (litellm.go:23)
  type ResultJSON struct{…}    // was litellmResultJSON (:29)
  type SyncPlanJSON struct{…}  // was syncPlanJSON (:72)
  var ErrIDFailed = errors.New("one or more models could not be applied") // was errLitellmIDFailed (:35)
  func Report(out, errOut io.Writer, res Result, asJSON bool) error                                // :40
  func ReportSyncPlan(out, errOut io.Writer, plan SyncPlan, warnings []string, asJSON bool) error  // :88
  func RunSync(out, errOut io.Writer, cfg *config.Config, probe Probe, asJSON, dryRun bool) error  // :173
  func SyncAfterWrite(out, errOut io.Writer, probe Probe) string                                   // model.go:34 realSyncRoutesAfterWrite
  func SyncAndWarn(out, errOut io.Writer, warnings []string, sync func(out, errOut io.Writer) string) error // model_write.go:243
  ```
  `cmd/wt-session` keeps (model.go): `func realSyncRoutesAfterWrite(out, errOut io.Writer) string { return litellm.SyncAfterWrite(out, errOut, probeInventory) }` and `var syncRoutesAfterWrite = realSyncRoutesAfterWrite`, both with their doc comments (the test helper `realRouteSync`, model_test.go:46-51, assigns `syncRoutesAfterWrite = realSyncRoutesAfterWrite` for its four callers in model_test.go, model_write_test.go and cloudsync_test.go, so `model_test.go` is unchanged and still compiles), `func syncAndWarn(out, errOut io.Writer, w []string) error { return litellm.SyncAndWarn(out, errOut, w, syncRoutesAfterWrite) }` (model_write.go; the seam is read per call), `func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error { return litellm.RunSync(out, errOut, cfg, probeInventory, asJSON, dryRun) }`.

- [ ] **Step 1: Write the failing tests** in `internal/litellm/report_test.go`:
  ```go
  func TestSyncAndWarnPrintsTheSyncLinesThenEveryWarning(t *testing.T) {
  	var out, errOut bytes.Buffer
  	sync := func(o, _ io.Writer) string { fmt.Fprintln(o, "synced"); return "LiteLLM routes not synced: x" }
  	if err := SyncAndWarn(&out, &errOut, []string{"first"}, sync); err != nil { t.Fatal(err) }
  	if out.String() != "synced\n" { t.Errorf("out = %q", out.String()) }
  	if errOut.String() != "warning: first\nwarning: LiteLLM routes not synced: x\n" { t.Errorf("errOut = %q", errOut.String()) }
  }
  func TestSyncAfterWriteSkipsAMachineWithoutLiteLLM(t *testing.T) {
  	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
  	probed := false
  	got := SyncAfterWrite(io.Discard, io.Discard, func(*config.Config) localmodels.Snapshot { probed = true; return localmodels.Snapshot{} })
  	if got != "" || probed { t.Fatalf("got %q, probed %v; want \"\" and no probe", got, probed) }
  }
  ```
  `TestLitellmSyncDryRunJSONMatchesContract`, `TestLitellmSyncJSONMatchesContract`, `TestSyncReportsTheAPIBaseRepairInText` stay in `cmd/wt-session/litellm_test.go` (they read the fixture; they move in 4c) and are edited to call `litellm.ReportSyncPlan`/`litellm.Report` and compare with `litellm.ErrIDFailed`; every `var x syncPlanJSON` / `litellmResultJSON` becomes `litellm.SyncPlanJSON` / `litellm.ResultJSON`.
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/litellm -count=1 -run 'TestSyncAndWarn|TestSyncAfterWrite'` → `undefined: SyncAndWarn`.
- [ ] **Step 3: Implement.** Cut the listed ranges into `internal/litellm/report.go` (rename per the list, drop `litellm.` qualifiers; `SyncAfterWrite` is `realSyncRoutesAfterWrite` with `runLitellmSync(...)` replaced by `RunSync(out, errOut, cfg, probe, false, false)`); add the cmd one-liners above (model.go keeps the named `realSyncRoutesAfterWrite`, never an anonymous closure). No output text changes; `grep -n 'realSyncRoutesAfterWrite' cmd/wt-session/model.go cmd/wt-session/model_test.go` still lists the definition, the var and `realRouteSync`'s assignment.
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/litellm -count=1`; cmd subset `-run 'Litellm|Sync|ModelAdd|ModelEdit|ModelRm|ModelInit|Editor|CloudSync'`; `go vet ./...`.
- [ ] **Step 5: Commit.** `refactor(litellm): share the route-sync printers and the after-write sync between both binaries` — files: `internal/litellm/report.go`, `internal/litellm/report_test.go`, `cmd/wt-session/litellm.go`, `cmd/wt-session/model.go`, `cmd/wt-session/model_write.go`, `cmd/wt-session/litellm_test.go`, `cmd/wt-session/litellm_dup_test.go`.

### Task 21: `config.ValidateSession` and the synthesized-agent write guard

**Files:**
- Modify: `internal/config/config.go:876-972` (validate's agent loop extracted; `ValidateSession` added after `ValidateAll`:885)
- Test: `internal/config/validate_session_test.go` (new), `internal/config/modelserver_test.go` (one new test)

**Interfaces:**
- Produces:
  ```go
  // ValidateSession reports, joined, every problem a session-side save can repair without a registry:
  // default_tag set; each agent named, unique, with at least one supported provider and its default
  // provider in that list. Provider ids are not checked; AllProviders agents are skipped.
  func (c *Config) ValidateSession() error
  // validateAgents is the one agent loop: known == nil skips the unknown-provider check.
  func (c *Config) validateAgents(known map[string]bool) []error
  ```
  `validate()` calls `c.validateAgents(provIDs)` (texts unchanged: `agent entry with empty name`, `duplicate agent name %q`, `agent %q: must have at least one supported provider`, `agent %q: unknown provider %q`, `agent %q: default provider %q not in supported_providers`).

- [ ] **Step 1: Write the failing tests.**
  ```go
  func TestValidateSessionChecksOnlyWhatConfigTomlOwns(t *testing.T) {
  	ok := &Config{DefaultTag: "code", Agents: []Agent{{Name: "claude", SupportedProviders: []string{"nosuch"}, DefaultProvider: "nosuch"}}}
  	if err := ok.ValidateSession(); err != nil { t.Fatalf("unknown provider id refused: %v", err) }
  	bad := &Config{Agents: []Agent{
  		{Name: "a", SupportedProviders: []string{"p"}}, {Name: "a", SupportedProviders: []string{"p"}},
  		{Name: "b"}, {Name: "c", SupportedProviders: []string{"p"}, DefaultProvider: "q"},
  		{Name: "synth", AllProviders: true}}}
  	got := bad.ValidateSession().Error()
  	want := "default_tag must not be empty\nduplicate agent name \"a\"\nagent \"b\": must have at least one supported provider\nagent \"c\": default provider \"q\" not in supported_providers"
  	if got != want { t.Fatalf("ValidateSession = %q\nwant %q", got, want) }
  }
  func TestValidateAllKeepsItsAgentErrors(t *testing.T) { // ValidateAll still names an unknown provider
  	c := &Config{DefaultTag: "code", Agents: []Agent{{Name: "a", SupportedProviders: []string{"nosuch"}}}}
  	if err := c.ValidateAll(); err == nil || err.Error() != `agent "a": unknown provider "nosuch"` { t.Fatalf("ValidateAll = %v", err) }
  }
  ```
  In `modelserver_test.go`:
  ```go
  func TestPatchSaveNeverWritesASynthesizedAgent(t *testing.T) {
  	// config.toml with only a codex agent; LoadSession("claude") adds claude in memory
  	// write config.toml = "default_tag = \"code\"\n\n[[agents]]\nname = \"codex\"\nsupported_providers = [\"openrouter\"]\n" under IsolateConfigHome's dir
  	cfg, err := LoadSession("claude") // → cfg.Agents has claude with AllProviders
  	err = cfg.PatchSave(func(fresh *Config) { fresh.Agents = cfg.Agents })
  	// assert: the file contains `name = "codex"` and does not contain `name = "claude"`
  }
  ```
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/config -count=1 -run 'TestValidateSession|TestValidateAllKeepsItsAgentErrors|TestPatchSaveNeverWritesASynthesizedAgent'` → `c.ValidateSession undefined` (the PatchSave test passes already: it pins `writeConfigFile`'s existing filter at config.go:1388).
- [ ] **Step 3: Implement** the two functions in `internal/config/config.go`; `ValidateSession` = `errors.Join(append(defaultTagErr, c.validateAgents(nil)...)...)` with `validateAgents` skipping `a.AllProviders` only when `known == nil` (ValidateAll never sees a synthesized agent: `config.Load` makes none).
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/config -count=1`.
- [ ] **Step 5: Commit.** `feat(config): add ValidateSession, the agent checks a session-side save can make without a registry` — files: `internal/config/config.go`, `internal/config/validate_session_test.go`, `internal/config/modelserver_test.go`.

### Task 22: `internal/modeview` — the mode/url/token/server lines, one copy

**Files:**
- Create: `internal/modeview/modeview.go`, `internal/modeview/modeview_test.go`
- Modify: `internal/configeditor/server_view.go:147-197` (modeSource, envNames, hasEnv, tokenLine), `:258-306` (serverKindWord, serverCheckLine, missingForCheck), `:356-407` (serverStatusLines composes modeview), `internal/depcheck/depcheck_test.go:45` (sessionLibraries), the `TestModeLeavesStayOffTheClient` list (added in 1a)

**Interfaces:**
- Consumes: `modelmode.State`, `modelmode.Resolution`, `modelmode.ServerKind` (1b/1c); `editorkit.Line`, `editorkit.Tone*`, `editorkit.Scrub`, `editorkit.HomePath` (PR 2).
- Produces (spine §5, exact):
  ```go
  package modeview // imports editorkit, modelmode, urlrule, stdlib
  func ModeSource(r modelmode.Resolution) string
  func EnvNames(st modelmode.State) []string
  func KindWord(k modelmode.ServerKind) string
  func ModeLine(st modelmode.State) editorkit.Line
  func URLLine(st modelmode.State) editorkit.Line
  func TokenLine(st modelmode.State, userHome string) editorkit.Line
  func ServerCheckLine(st modelmode.State) editorkit.Line
  func EnvLine(st modelmode.State) (editorkit.Line, bool)
  func ProblemLines(st modelmode.State) []editorkit.Line
  ```

- [ ] **Step 1: Write the failing tests** in `internal/modeview/modeview_test.go` (values copied from server_view.go):
  - `TestModeSourceWords`: `SourceEnvMode`→`"from WT_MODEL_MODE"`, `SourceEnvEndpoint`→`"from WT_MODEL_URL with a token"`, `SourceStored`→`"from config.toml"`, `SourceInferred`→`"inferred: a url or token is set"`, `SourceNone`→`""`.
  - `TestKindWords`: `KindUnauthorized`→`"token refused"`, `KindNotWtModel`→`"not a wt-model server"`, `KindSkew`→`"api version differs"`, `KindSchema`→`"schema differs"`, `KindUnreachable`→`"unreachable"`.
  - `TestModeLineCases`: `Resolution{Mode: "remote", Source: SourceStored}` → `Line{"mode", "remote (from config.toml)", ToneNone}`; `Config.ParseErr: "x"` → `{"mode", "unknown: config.toml is not readable", ToneWarn}`; `Resolution{Problem: "WT_MODEL_MODE=x is not a mode"}` → `{"mode", "problem: WT_MODEL_MODE=x is not a mode", ToneBad}`; zero → `{"mode", "unset: choose one below", ToneWarn}`.
  - `TestURLLineScrubsAndSaysNone`: empty url → `{"url", "none set", ToneNone}`; `https://u:p@h:8787` → text equals `editorkit.Scrub("https://u:p@h:8787")` and does not contain `"u:p"`.
  - `TestTokenLineWords`: no path + `EnvOverride: "WT_MODEL_TOKEN"` → `"from WT_MODEL_TOKEN (not shown)"`; no path → `{"token", "none set", ToneDim}`; `Path "/Users/user_name/.config/t"`, `Exists, Loose` with userHome `/Users/user_name` → `{"token", "~/.config/t  loose: chmod 600 ~/.config/t", ToneBad}`; `Err "x"` → `"…  problem: x"`; `!Exists` → `"…  missing"`; ok → `"~/.config/t  ok"`.
  - `TestServerCheckLineWords`: native → `{"server", "none in native mode", ToneDim}`; `Server{Checked:false, Message:"not checked: the url contains an @; retype it without credentials"}` → text `"not checked: the url contains an @; retype it without credentials"` (no double prefix); unchecked remote with no url and no token → `"not checked: no url and no token"`; OK → `"ok: wt-model answers at http://127.0.0.1:8787 (api 1, schema 1)"`; failed Kind `KindSkew` Message `"m"` → `{"server", "problem: api version differs: m", ToneBad}`.
  - `TestEnvAndProblemLines`: `EnvOverride "WT_MODEL_URL, WT_MODEL_TOKEN"` → `EnvLine` = `{"env", "WT_MODEL_URL, WT_MODEL_TOKEN set: they override config.toml", ToneWarn}, true`; none → `false`. `ProblemLines` of `TableErr "t"`, `RestErr "r"`, `Problems ["p"]` → `[{"problems","[model-server]: t",ToneBad}, {"","config.toml: r",ToneBad}, {"","p",ToneBad}]`.
  In `depcheck_test.go`: add `"internal/modeview"` to `sessionLibraries` and to the closure list of `TestModeLeavesStayOffTheClient`.
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/modeview -count=1` → `no Go files` / undefined; `go test ./internal/depcheck -count=1` → `internal/modeview is named in depcheck_test.go but no longer exists`.
- [ ] **Step 3: Implement** `internal/modeview/modeview.go` by moving the bodies listed (server_view.go:147-197, :258-306, :358-374 mode+url, :386-405 env+problems) and typing them on `modelmode.State`; `TokenLine`'s abbreviation is `editorkit.HomePath(t.Path, st.Host.Home, userHome)`. `configeditor/server_view.go`'s `serverStatusLines` keeps its order and becomes: `ModeLine(st.State)`, `URLLine(st.State)`, `TokenLine(st.State, m.home)`, pair, agent, `ServerCheckLine(st.State)`, litellm, plist, `EnvLine`, `ProblemLines` (a local server's url is never "missing" stays in `missingForCheck`, now in modeview). Delete the moved functions from configeditor.
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/modeview ./internal/configeditor ./internal/depcheck -count=1` (every `TestServerTab*` passes unchanged — same text).
- [ ] **Step 5: Commit.** `refactor(wt): add the modeview leaf for the mode, url, token and server lines both editors show` — files: `internal/modeview/*`, `internal/configeditor/server_view.go`, `internal/depcheck/depcheck_test.go`, `docs/internals/packages.md` (one row for `internal/modeview`).

---

### PR 3b: `modeleditor` (Models tab), `wt-model model`, bare `wt model` becomes a pointer

- **Entry:** PR 3a merged (`litellm.SyncAndWarn`, `litellm.SyncAfterWrite`, `litellm.DesiredRouteIDsFor`, `modeview` exist).
- **Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (background), naming `go test ./internal/depcheck ./internal/removal ./internal/modeleditor ./internal/configeditor ./cmd/wt-model -count=1` and `go test ./cmd/wt-session -count=1` in the PR body.
- **Size:** ~1,200 changed + ~2,800 moved (git mv; the squash diff shows renames).

### Task 23: `movedError` — the pointer line, ahead of the stub table

**Files:**
- Create: `cmd/wt-session/moved.go`, `cmd/wt-session/moved_test.go`

**Interfaces:**
- Produces (spine §10; only `movedError` now — `movedCommand`, `movedCommands`, `movedStub`, `addMovedStubs` are 4a-i's):
  ```go
  // movedError is the one wording of a command that moved to wt-model: exit 1, one line, nothing changed.
  func movedError(old, new string) error // exitcode.New(1, errors.New(old+" moved to wt-model — run 'wt-model "+new+"'; nothing was changed"), false)  (exitcode.New takes an error)
  ```
- [ ] **Step 1: Write the failing test** `TestMovedErrorIsOneLineExitOne`:
  ```go
  err := movedError("model", "model")
  var buf bytes.Buffer
  code := exitcode.Report("wt", err, &buf)
  if code != 1 || buf.String() != "wt: model moved to wt-model — run 'wt-model model'; nothing was changed\n" { t.Fatalf("code %d, %q", code, buf.String()) }
  ```
  (Check `exitcode.New`'s third argument and `Report`'s prefix with `go doc ./internal/exitcode` before writing; the line above is the spine's wording.)
- [ ] **Step 2: Run, expect FAIL.** `go test ./cmd/wt-session -count=1 -run TestMovedErrorIsOneLineExitOne` → `undefined: movedError`.
- [ ] **Step 3: Implement** `movedError` in `cmd/wt-session/moved.go`.
- [ ] **Step 4: Run, expect PASS.** same command.
- [ ] **Step 5: Commit.** `feat(wt): add the moved-to-wt-model pointer error` — files: `cmd/wt-session/moved.go`, `cmd/wt-session/moved_test.go`.

### Task 24: `internal/modeleditor` takes the Models tab; `configeditor` keeps Agents + Model server; bare `wt model` points at `wt-model model`

One atomic task: `git mv` removes the Models files from `configeditor`, so its shell, `cmd/wt-session`'s two openers and the tests move in the same commit or the tree does not build.

**Files:**
- Move (`git mv`): `internal/configeditor/models_tab.go` → `internal/modeleditor/models_tab.go`; `models_form.go` → `modeleditor/models_form.go`; `models_tab_test.go`, `models_form_test.go`, `models_pairing_test.go` → `modeleditor/`
- Create: `internal/modeleditor/editor.go` (shell), `internal/modeleditor/tabs.go`, `internal/modeleditor/testmain_test.go` (copy of `configeditor/testmain_test.go`), `internal/modeleditor/editor_test.go`, `internal/configeditor/filter.go`, `internal/configeditor/helpers_test.go`
- Modify: `internal/configeditor/editor.go` (whole shell: lines 32-92 model fields, 101-115 Init, 124-132 Update, 183-262 update, 328-412 quit/leave/refuseHeldQuit, 414-439 View, 458-467 Run), `internal/configeditor/tabs.go:14-135`, `internal/configeditor/server_tab.go:557-594` (finishServerWrite, switchTab), `internal/configeditor/editor_test.go`, `internal/configeditor/quit_test.go`, `internal/configeditor/server_tab_test.go:18-54`, `cmd/wt-session/commands_config.go:1-80`, `cmd/wt-session/model.go:56-73`, `cmd/wt-session/model_test.go:460-560`, `cmd/wt-session/commands_config_test.go:60-130,327-356`, `cmd/wt-session/main_test.go:24-40`, `internal/depcheck/depcheck_test.go:16-22,234-243`

**Interfaces:**
- Consumes: `editorkit.TabBar`, `editorkit.StatusBlock`, `editorkit.WrapText`, `editorkit.TildePath` (PR 2); `movedError` (Task 23).
- Produces (spine §7; `TabSetup`, `SetupDeps` and `Options.Setup` are added in Task 27):
  ```go
  package modeleditor
  type Tab int
  const TabModels Tab = iota
  var tabNames = []string{"Models"}
  type ModelsDeps struct{ Load func() (*config.Config, error); Probe func(*config.Config) localmodels.Snapshot
  	SeedEnv func() config.SeedEnv; Capabilities func(cfg *config.Config, name string) (map[string]any, error) } // tabs.go:97, unchanged + withDefaults
  type Options struct{ StartTab Tab; Models ModelsDeps; UserHome func() (string, error) }
  type Result struct{ RegistryChanged bool }
  func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error)
  ```
  `configeditor` after this task: `const (TabAgents Tab = iota; TabServer)`, `tabNames = []string{"Agents", "Model server"}`, `Options{StartTab; Server ServerDeps; UserHome}`, `type Result struct{}` (no `RegistryChanged`), `Run` unchanged otherwise. `configeditor/filter.go` holds its own copy of `filterMatchesMsg`, `tagFilter`, `applyFilterMatches` (models_tab.go:594-632; agents only), since the agents list uses them and their file moved.
  `cmd/wt-session`: `runConfigEditor(out, errOut io.Writer, a *app, start configeditor.Tab) error` keeps its signature, passes only `Server`, and no longer syncs routes; `modelCmd`'s `RunE` returns `movedError("model", "model")` on any terminal state.

- [ ] **Step 1: Write the failing tests.**
  - `internal/modeleditor/editor_test.go`:
    - `TestRunOpensOnTheModelsTabAndProbesAtOnce`: `m := newModel(testTheme(), nil, nil)`, `m.opts = Options{Models: tm.deps()}` (`tabMachine` from the moved `models_tab_test.go`); `cmd := m.Init()`; running it (`runCmd`) yields one probe (`tm.probes == 1`) and `View()` starts with `"[Models]"`.
    - `TestTabBarShowsOnlyModels`: `tabBar` output contains `"[Models]"` and not `"Agents"` nor `"Model server"`.
    - `TestQuitLeavesAtOnceWithNoAgentPrompt`: after load, `q` returns `tea.Quit` (no `phaseQuit`; there is no save key or agent edit here).
    - `TestLoadedMsgLocationErrorNamesTheRegistry` moves here from `configeditor/editor_test.go:203` (the shell's load banner keeps `config.RegistryFixHint`).
  - Moved tests, kept by name: everything in `models_tab_test.go`, `models_form_test.go`, `models_pairing_test.go` **except** the Agents-tab or cross-tab ones, which are rewritten or left in configeditor as below.
    - `TestTabKeySwitchesTabsAndProbesOnce` → split: modeleditor keeps the probe-once part as `TestModelsProbeRunsOnceUntilR` (Init probes once, `r` probes again, a second probe is not started by any other key); the Agents half is dropped (there is no Models tab to switch to in configeditor).
    - `TestAgentsFilterNarrowsTheList` → `configeditor/editor_test.go` (unchanged body; uses the helpers from `configeditor/helpers_test.go`).
    - `TestEscOnAListDoesNotQuit` → split by tab: Models half stays in modeleditor, Agents half moves to `configeditor/quit_test.go` as `TestEscOnTheAgentsListDoesNotQuit`.
    - `TestQuitPromptFitsANarrowTerminal`, `TestAFailedQuitSaveIsShownOnTheAgentsTab`, `TestAgentsStatusFollowsTheLastSave` → `configeditor/quit_test.go` (drop their Models-tab steps; the assertion values are unchanged).
    - `TestADiscardHeldForTwoWritesSurvivesTheFirst` → rewritten in configeditor as `TestADiscardHeldForAServerApplySurvivesItsFailure` (one write in flight now: a Model server apply; after `q`,`n` and a failed apply the quit is called off and `m.dirty == true`).
    - `TestQuitWaitsForARegistryWriteInFlight`, `TestAHeldQuitAfterAFormSaveIsAnOrdinaryQuit`, `TestQuitWaitsForAFormSaveInFlight`, `TestAHeldQuitIsShownOnTheModelsTab` stay in modeleditor, with every agent-edit step removed: the held quit then ends with `tea.Quit` once the write reports, and `Result.RegistryChanged == true`.
    - `TestCtrlSOnTheModelsTabDoesNothing` stays (ctrl+s is inert, no save key).
    - `TestWrapTextWithoutAWidth` is already editorkit's after PR 2 (delete any leftover).
  - `configeditor/server_tab_test.go`: `TestTabBarShowsThreeTabs` → `TestTabBarShowsAgentsAndModelServer` (`tabBar(theme, TabServer)` contains `"Agents"` and `"[Model server]"` and not `"Models"`); `TestNextTabCycles` map becomes `{TabAgents: TabServer, TabServer: TabAgents}`.
  - `configeditor/helpers_test.go`: copies of the test helpers the remaining configeditor tests use from the moved file (`send`, `keys`, `keyMsg`, `sendKey`, `assertFits`, `flat`, `plain` if it was there) — a test file cannot be shared across packages.
  - `cmd/wt-session/model_test.go`: `TestBareModelOpensTheModelsTab`, `TestEditorSyncsRoutesOnceOnExit`, `TestEditorExitSyncUnderARedirectedRegistry` are deleted here (their wt-model twins are Task 25); new:
    ```go
    func TestBareWtModelPointsAtWtModel(t *testing.T) {
    	for _, tty := range []bool{true, false} {
    		registry := modelHome(t, writeRegistry); stubStdinTTY(t, tty)
    		cfgBefore, regBefore := mustRead(t, filepath.Join(config.Dir(), "config.toml")), mustRead(t, registry)
    		call := stubConfigEditor(t, configeditor.Result{}, nil)
    		out, err := runWT(t, "model")
    		// err's exit code 1 (exitcode), the reported line == "wt: model moved to wt-model — run 'wt-model model'; nothing was changed"
    		// call.called == false; both files byte-identical
    	}
    }
    ```
    (Read the config.toml bytes only if `modelHome` writes one; otherwise assert it does not exist before and after.)
  - `cmd/wt-session/commands_config_test.go`: `TestConfigLongTextNamesThreeTabs` → `TestConfigLongTextNamesTwoTabsAndTheModelsMove`: Long contains `"two tabs"`, `"Agents"`, `"Model server"`, `"wt-model model"`, and not `"three tabs"`.
  - `internal/depcheck/depcheck_test.go`: add `"internal/modeleditor"` to `adminBanned` and append it to the `adminScreens` list Task 13 introduced, which becomes `[]string{"internal/configeditor", "internal/modeleditor", "internal/editorkit"}` (the loop and its message are Task 13's; nothing else changes).
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/modeleditor -count=1` → `no non-test Go files`/undefined `newModel`; `go test ./cmd/wt-session -count=1 -run 'TestBareWtModelPointsAtWtModel|TestConfigLongText'` → the editor is opened / old Long text.
- [ ] **Step 3: Implement.**
  - `internal/modeleditor/editor.go`: a new shell copied from `configeditor/editor.go` and trimmed to the Models tab: fields `tab, opts, models modelsTab, registryChanged, quitPending, theme, cfg, cfgErr, width, height, ready, status, home string`; no `list`, agent form, `dirty`, `saving`, `quitting`, `discardHeld`, `phaseQuit`, `phaseDelete`. `Init` returns `tea.Batch(loaded, m.probeCmd())`. `quit()` = `leave()`; `leave()` holds the quit while `m.models.writing` (as editor.go:349-364, without the server branch). `refuseHeldQuit` keeps only the `quitPending` reset. `Run` resolves `home` once from `o.UserHome` (default `os.UserHomeDir`), fills `o.Models.withDefaults()`, returns `Result{RegistryChanged: m.registryChanged}`.
  - `internal/modeleditor/tabs.go`: `Tab`, `TabModels`, `tabNames`, `ModelsDeps` + `withDefaults` (configeditor/tabs.go:92-124 moved), `Options`, `Result`, `tabBar(theme, current) string { return editorkit.TabBar(theme, tabNames, int(current)) }`.
  - `models_tab.go`/`models_form.go`: package clause `modeleditor`; drop the `case m.tab == TabModels` branches that switched to Agents (tab key inert).
  - `configeditor`: delete `models`/`registryChanged` fields and every `modelsLoadedMsg`/`modelRemovedMsg`/`modelSavedMsg` case; `leave()` keeps only the `m.server.writing` branch; `refuseHeldQuit` keeps `discardHeld`; `handleQuitUpdate`'s `n` checks `m.server.writing` only; `switchTab` drops the Models case; `View` drops the Models branch; `Run` drops `o.Models` and returns `Result{}`; package doc (editor.go:1-5) says "an Agents tab ... and a Model server tab".
  - `cmd/wt-session/commands_config.go`: doc comment lines 1-5 and `runConfigEditor` (lines 33-56) lose the Models tab and the route sync; `Long` becomes:
    ```
    "Manage wt user preferences.\n\n" +
    "With no subcommand, launches an interactive TUI with two tabs (tab\n" +
    "switches): Agents, the agents in config.toml, saved with ctrl+s; and\n" +
    "Model server, which checks the wt-model server and its LiteLLM proxy and\n" +
    "sets up the server's address, token and proxy key. The models in\n" +
    "registry.toml are managed with `wt-model model`.\n\n" +
    "Subcommands:\n  theme   active color theme\n  path    print the config directory"
    ```
  - `cmd/wt-session/model.go:56-73`: `Long` → `"Manage the models in the registry.\n\nWith no subcommand, prints where the Models screen moved: `wt-model model`.\nThe subcommands work without a terminal."`; `RunE` → `return movedError("model", "model")` (drop the `stdinTTY` branch and the `configeditor` import).
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/modeleditor ./internal/configeditor ./internal/depcheck -count=1`; cmd subset `go test ./cmd/wt-session -count=1 -run 'Model|Config|Gate|Editor|Moved|NoWtCommandIsHidden|RootHelp'`; `go vet ./... && gofmt -l .`
- [ ] **Step 5: Commit.** `refactor(wt): move the Models tab into internal/modeleditor; bare wt model points at wt-model model` — files: every file named in **Files** above.

### Task 25: `wt-model model` opens the Models tab and runs the one route sync

**Files:**
- Create: `cmd/wt-model/host.go`, `cmd/wt-model/seams.go`, `cmd/wt-model/model_tui.go`, `cmd/wt-model/model_tui_test.go`, `cmd/wt-model/helpers_test.go`
- Modify: `cmd/wt-model/main.go:1-2` (package comment: "Command wt-model is wt's host side: it serves the model control API and gateway, and administers the registry, the LiteLLM routes and this machine's model-server setup"), `:24-36` (rootCmd adds the command; root `Short` becomes `"Serve wt's models and administer the registry, routes and this machine's setup"`), `cmd/wt-model/testmain_test.go` (stubs)

**Interfaces:**
- Consumes: `modeleditor.Run/Options/ModelsDeps/Result/TabModels` (Task 24); `litellm.SyncAndWarn`, `litellm.SyncAfterWrite` (3a).
- Produces:
  ```go
  // host.go — from cmd/wt-session/app.go:215-252, trimmed to what wt-model needs
  // field and method names as in cmd/wt-session/app.go, the names Task 37, 38, 42, 45, 50 and 59 consume
  type host struct{ hostCfg *config.Config; hostLoadErr, hostCfgErr error; hostSet bool }
  func (h *host) hostConfig() (*config.Config, error)  // config.Load once; never nil (DefaultTag "code" stand-in)
  func (h *host) hostConfigErr() error                 // load error, else cfg.Validate()
  func loadTheme() (themes.Theme, error)           // themes.Load; a themes.IsThemeNameError falls back to the returned default, nil
  // seams.go — package-var seams (the documented migrating exception, spine §15.18); 4a-ii keeps one declaration
  // of each var: Task 37 and 38 delete the duplicates the git-mv'd model_list.go/model_write.go bring.
  var probeInventory = localmodels.Inventory
  var seedEnv = config.DefaultSeedEnv
  var ollamaCaps = modeladmin.OllamaCapabilities
  func realSyncRoutesAfterWrite(out, errOut io.Writer) string { return litellm.SyncAfterWrite(out, errOut, probeInventory) } // as cmd/wt-session/model.go after Task 20
  var syncRoutesAfterWrite = realSyncRoutesAfterWrite
  var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) } // cmd/wt-session/helpers.go:128
  var modeleditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) { return modeleditor.Run(theme, cfg, cfgErr, o) }
  // model_tui.go
  func modelCmd(h *host) *cobra.Command                                  // `wt-model model`, bare: the Models tab
  func runModelEditor(out, errOut io.Writer, h *host, start modeleditor.Tab) error
  ```
  `runModelEditor`: theme via `loadTheme`; `modeleditorRun(theme, cfg, h.hostConfigErr(), modeleditor.Options{StartTab: start, Models: modeleditor.ModelsDeps{Probe: probeInventory, SeedEnv: seedEnv, Capabilities: ollamaCaps}})`; `if res.RegistryChanged { _ = litellm.SyncAndWarn(out, errOut, nil, syncRoutesAfterWrite) }`; returns the editor's error. `modelCmd` without a terminal returns `errors.New("wt-model model needs a terminal to open the Models tab; without one use `wt model list`, `wt model add`, `wt model edit` or `wt model rm`")` (4a-ii rewrites the names). `Use: "model"`, `Short: "Manage the model registry (registry.toml)"`, `Args: cobra.NoArgs`, `SilenceUsage: true`. Bare `wt-model` stays cobra help (no `RunE` on root).

- [ ] **Step 1: Write the failing tests** in `cmd/wt-model/model_tui_test.go` (helpers in `helpers_test.go`: `stubStdinTTY`, `stubModelEditor(t, res, err) *editorCall`, `stubRouteSync(t, warning) *int`, `modelHome(t, registry)`, `redirectedRegistry(t, …)` copied from `cmd/wt-session/*_test.go`; `execWTModel(t, args...) (stdout, stderr string, err error)` over `rootCmd(fakeDeps)`):
  - `TestWtModelModelOpensTheModelsTab` (twin of `TestBareModelOpensTheModelsTab`, model_test.go:463): `call.opts.StartTab == modeleditor.TabModels`; the handed `Probe`, `SeedEnv`, `Capabilities` are the package seams (same marker values: `"marker/probe"`, `"marker-seed"`, `"marker_caps"`); `*syncs == 0`.
  - `TestWtModelModelSyncsRoutesOnceOnExit` (twin of model_test.go:503, `wt-model model` only): nothing changed → 0 syncs; `RegistryChanged` → 1 sync and stderr contains `"warning: LiteLLM routes not synced: proxy config is read-only"`; editor error after a write → the error and still 1 sync.
  - `TestWtModelModelExitSyncUnderARedirectedRegistry` (twin of model_test.go:540): with the real `syncRoutesAfterWrite`, the output is exactly `"warning: LiteLLM routes not touched: the registry is "+registry+" but config.yaml is the default "+defaultYAML+" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"` and the default `config.yaml` stays `"model_list: []\n"`; named, that file is the one synced.
  - `TestWtModelModelWithoutATerminalSaysSo`: `stubStdinTTY(t, false)` → error text starts `"wt-model model needs a terminal"`, editor not called.
  - `TestBareWtModelStillPrintsHelp`: `execWTModel(t)` → stdout contains `"serve"`, `"litellm"`, `"model"` and the new root Short `"Serve wt's models and administer the registry, routes and this machine's setup"`, err nil, editor not called.
  `testmain_test.go` gains, before `m.Run()`, the exact stubs `cmd/wt-session/testmain_test.go` installs (:54, :94-102), with their comments: `probeInventory = localmodels.OnDiskSnapshotForTest`; `ollamaCaps = func(*config.Config, string) (map[string]any, error) { return nil, errors.New("ollamaCaps not stubbed in this test") }`; `seedEnv = func() config.SeedEnv { return config.SeedEnv{} }`; `syncRoutesAfterWrite = func(io.Writer, io.Writer) string { return "syncRoutesAfterWrite not stubbed in this test" }` (tests that expect a sync stub it with `stubRouteSync`). Tasks 37 and 38 then find these present and add only `stdoutWidth`, `confirmRemove` and the proxy-readiness stub.
- [ ] **Step 2: Run, expect FAIL.** `go test ./cmd/wt-model -count=1 -run 'TestWtModelModel|TestBareWtModel'` → `undefined: modelCmd`.
- [ ] **Step 3: Implement** the three files and `h := &host{}` built once in `rootCmd` and `root.AddCommand(serveCmd(mk), litellmCmd(), modelCmd(h))` in `main.go:35` (every later wt-model command takes the same `h`).
- [ ] **Step 4: Run, expect PASS.** `go test ./cmd/wt-model ./internal/depcheck -count=1` (`TestModelServerBinaryStaysOffTheSessionPackages` must pass: modeleditor links no `sessionBanned` package).
- [ ] **Step 5: Commit.** `feat(wt-model): open the Models tab with wt-model model` — files: `cmd/wt-model/{host,seams,model_tui,main}.go`, `cmd/wt-model/{model_tui,helpers,testmain}_test.go`.

### Task 26: docs for 3b

**Files:** `docs/wt-config.md:5-30` (three tabs → two; the Models section becomes a pointer to `wt-model model`), `docs/wt-model.md` (bare `wt model` prints the pointer, exit 1), `docs/wt-model-server.md` (a "`wt-model model`" section: opens the Models tab, needs a terminal, one route sync on exit), `docs/configuration.md` (the Models-tab mention), `docs/internals/packages.md` (rows for `internal/modeleditor`, `cmd/wt-model/{host,seams,model_tui}.go`; configeditor row says Agents + Model server), `CLAUDE.md` (wt: "Import graph ratchet" names `modeleditor` among the admin packages and the `adminScreens` loop), `../CLAUDE.md` (the `wt-model` bullet lists `model`), `../docs/guides/00-config-map.md` and `../docs/guides/01-initial-setup.md` where they say "Models tab of `wt config`"; and every place that describes **bare** `wt model` (the editor), which Step 1's second grep finds — today `../docs/guides/00-config-map.md:47,340`, `../docs/guides/02-providers-and-models.md:1,40,44,66`, `../docs/guides/03-model-families.md:29` ("(or: wt model, then enter on the row)" → "(or: `wt-model model`, then enter on the row)"), `README.md:107,126` (the `wt model` row and the three-tab sentence), `docs/wt-model.md:1,356`, `docs/wt-model-server.md:9`, `docs/wt-config.md:21`, `docs/internals/packages.md:12,31`.

- [ ] **Step 1:** `grep -rn -e 'Models tab' -e 'three tabs' -e 'opens the editor on this tab' docs ../docs/guides CLAUDE.md ../CLAUDE.md` — list every hit. Then the bare-name grep: `cd .. && git grep -nE '(^|[^-[:alnum:]_/])wt model([^ [:alnum:]-]|$| \()' -- '*.md' ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md'`. A hit whose `wt model` ends a line and continues with `add|edit|rm|list|init` on the next is a subcommand mention (Task 41's, 4a-ii) — leave it; every other hit names the bare command.
- [ ] **Step 2:** edit each hit to the 3b state: bare `wt model` is a pointer and the Models tab is `wt-model model` (no live model state; home paths as `/Users/user_name`). The `wt model` registry subcommands, `wt/CLAUDE.md:316` and `docs/style-guide.md` D2 stay until 4a-ii (Task 41), when the subcommands move.
- [ ] **Step 3:** `make -C .. check-links` → exit 0.
- [ ] **Step 4:** re-run both Step 1 greps → only hits that now say `wt-model model` (no "three tabs", no "Models tab of `wt config`"), and bare-name hits that are only line-wrapped subcommand mentions or a sentence saying bare `wt model` prints a pointer.
- [ ] **Step 5: Commit.** `docs(wt): the Models tab moved to wt-model model` — files: the edited docs.

---

### PR 3c: the Setup tab and `wt-model setup`; `sessioneditor` (Agents + Mode); `wt config` and the first-run gate switched; `configeditor` deleted

- **Entry:** PR 3b merged (`modeleditor` with the Models tab, `wt-model model`, `movedError`).
- **Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (background), naming `go test ./internal/depcheck ./internal/removal ./internal/sessioneditor ./internal/modeleditor ./internal/modelmode ./cmd/wt-model -count=1` and `go test ./cmd/wt-session -count=1`; plus `go list -deps ./cmd/wt-session | grep -E 'internal/(configeditor|modeleditor|modelsetup)$'` → no output (recorded in the PR body; the binary ratchet test itself is 4d-ii); plus `cd wt && make single-p` (wt-ci's single-p job runs it; the list changed in Task 31).
- **Size:** ~1,800 new/changed + ~4,000 moved (spine §13: one PR, reviewed commit by commit: Task 27–2 = (i) Setup, Task 29–4 = (ii) Mode tab, Task 31–6 = (iii) switch + delete). Files that leave `configeditor` are **copied** in Task 27/29 and the originals deleted in Task 31, so every commit builds; the squash-merged diff records them as renames (git's similarity detection over the PR diff).

### Task 27: the Setup tab in `modeleditor` (local and native only)

**Files:**
- Create (copied from configeditor, then edited): `internal/modeleditor/setup_tab.go` ← `server_tab.go`, `setup_view.go` ← `server_view.go`, `setup_litellm.go` ← `server_litellm.go`; tests `setup_tab_test.go` ← `server_tab_test.go` (admin cases), `setup_litellm_test.go` ← `server_litellm_test.go`, `setup_aturl_test.go` ← `server_aturl_test.go` (pair cases), `setup_polish_test.go` ← `server_polish_test.go` (admin cases)
- Modify: `internal/modeleditor/editor.go`, `internal/modeleditor/tabs.go`
- Create: `internal/modelsetup/twoeditors_test.go` (`TestModeTabRemoteThenSetupTabLocalAgree`, Step 1)

**Interfaces:**
- Consumes: `modelsetup.Observe/BuildPlan/Apply/Control/DefaultEnv/State/Desired/Plan/Result/ControlAction` (1c), `modeview.*` (3a), `editorkit.*` (2).
- Produces (spine §7):
  ```go
  const (TabModels Tab = iota; TabSetup)
  var tabNames = []string{"Models", "Setup"}
  type SetupDeps struct{ Observe func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State
  	Plan func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error)
  	Apply func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result
  	Control func(ctx context.Context, a modelsetup.ControlAction) error }        // configeditor/tabs.go:47
  func SetupDepsFromEnv(env modelsetup.Env) SetupDeps                            // tabs.go:60
  type Options struct{ StartTab Tab; Models ModelsDeps; Setup SetupDeps; UserHome func() (string, error) }
  var setupModes = []modelmode.Mode{modelmode.ModeLocal, modelmode.ModeNative}   // was serverModes, server_tab.go:596 (remote dropped)
  const remoteElsewhere = "remote mode is set in `wt config` (its Mode tab), not here"
  ```
  The shell: first visit of `TabSetup` observes (configeditor's `switchTab`); `leave()` holds the quit while `m.models.writing` **or** `m.setup.writing` (on that tab); `finishSetupWrite` = `finishServerWrite` without `discardHeld`. A stored or env **remote** mode is shown (mode line via `modeview.ModeLine`) and the mode choice starts on `local`; the status block adds one `ToneDim` line `{"", remoteElsewhere}` under the mode line when the resolved mode is remote. The remote fields (`sfURL`, `sfRemoteToken`, `sfPaste`) and `promptSaveAnyway` are deleted from this copy.

- [ ] **Step 1: Write the failing tests.** Copy and rename (`TestServerTab*` → `TestSetupTab*`, `serverMachine` → `setupMachine`, `ServerDeps` → `SetupDeps`, `TabServer` → `TabSetup`) these from configeditor: from `server_tab_test.go`: `TestServerTabObservesOnFirstVisitOnly`, `…ShowsEachLiteLLMCheck`, `…ShowsAParseErrorWithThePath`, `…DropsAStaleObservation`, `…LocalDisabledOffMacOS`, `…FitsTheTerminal`, `TestCtrlSOnTheServerTabDoesNothing`, `…QuitKeysUseTheExistingQuit`, `TestServerDepsWithDefaultsFillsOnlyNilFuncs`, `…ViewIsASCII`, `…NeverShowsASecret`, `…AlwaysReObservesBeforePlanning`, `…DisablesWritesOnAParseError`, `…ConfirmIsDefaultNo`, `…AppliesAfterY`, `…RefusalIsShownVerbatim`, `…AsksForTheMasterKeyWhenNeitherPairNorPlistHasOne`, `…LeavingLocalAsksAboutRemovingTheAgentDefaultNo`, `…QuitWaitsForAnApplyInFlight`, `…MasksThePastedTokenAndTheKey` (master-key half only), `…StartStopRestartCallControl`, `…FitsTheTerminalInEveryPhase` (local/native phases), `…RealPlanNeverShowsTheSecrets` (local plan), `…FormIsInertWhileAPlanIsPrepared`, `…PlansTheSnapshotTakenAtA`, `…ScrubsTheResolutionProblem`, `…HintsNameOnlyKeysThatAct`, `…SaysWhenStoredLocalCannotRunHere`, `…CallsApplyAndControlOnlyFromCommands`, `…SourceNeverFormatsAPlanWithV` (scans `setup_*.go`), `…NeverRendersAPieceOfAnAtURL` (local `Format` surface); all of `server_litellm_test.go`; from `server_aturl_test.go`: `TestServerTabNeverShowsOrRequestsAnAtPairURL`, `TestPairLineHidesAnAtURLItWasHanded` (+ its fakes `recNet`, `tmpHost`, `noLaunchd`, `noLiteLLM`, typed on modelsetup's widened seams); from `server_polish_test.go`: `…ParseErrorSaysTheErrorOnce`, `…ParseErrorPathIsAbbreviatedAndBreaksAtSlashes`, `…ClippedViewKeepsRadiosHintsAndWholeRows`, `…DropsLowestPriorityRowsFirstAndMarksTheEnd`, `…CancelsAPlanWhenCOrRReadsAgain`, `…NamesWhatACheckIsMissing`, `…PromptsStayVisibleAtEverySize`, `…CancelsAPlanInFlightWhenCOrRReadsAgain`. New:
  ```go
  func TestSetupTabOffersLocalAndNativeOnly(t *testing.T) {
  	m := setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 100, 30)
  	radios, _ := m.modeChoice()
  	// radios name "local" and "native", in that order, and never "remote"
  	m = keys(t, m, "right", "right", "right")
  	// m.setup.want.Mode cycles local → native → local; never remote
  }
  func TestSetupTabSaysRemoteIsSetInWtConfig(t *testing.T) {
  	st := fullState(); st.Resolution = modelmode.Resolution{Mode: modelmode.ModeRemote, Source: modelmode.SourceStored}
  	m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 100, 30)
  	if !strings.Contains(plain(m), "remote mode is set in `wt config` (its Mode tab), not here") { t.Fatal(plain(m)) }
  	if m.setup.want.Mode != modelmode.ModeLocal { t.Fatalf("choice = %q, want local", m.setup.want.Mode) }
  }
  func TestSetupTabHoldsAQuitForAModelsWriteToo(t *testing.T) // models.writing → q is held on TabModels; setup.writing → held on TabSetup
  ```
  `TestTabBarShowsOnlyModels` (Task 24) becomes `TestTabBarShowsModelsAndSetup` (`tabBar(theme, TabSetup)` contains `"Models"` and `"[Setup]"`).
  - `internal/modelsetup/twoeditors_test.go`, over modelsetup's fake environment (`fakes_test.go`) and one scratch `config.toml` (the anti-goal "half-configured state from two editors"; package `modelsetup`, which may import `modelmode`):
  ```go
  // TestModeTabRemoteThenSetupTabLocalAgree pins that the two editors read and
  // write one [model-server] table consistently: what the Mode tab (wt config)
  // stores as remote, the Setup tab (wt-model setup) observes as remote and
  // replaces with a plan that says so, never a half-configured mix.
  func TestModeTabRemoteThenSetupTabLocalAgree(t *testing.T) {
  	// 1. the Mode tab's path: p := modelmode.BuildPlan(<modelmode.Observe over the fake>, modelmode.Desired{Mode: ModeRemote,
  	//    URL: "https://gpu.example:8787", PastedToken: <a test token>, SaveUnchecked: true}); modelmode.Apply(ctx, env.Mode(), p, nil) → no error
  	// 2. st := Observe(ctx, env, ObserveOptions{}) → st.Resolution.Mode == ModeRemote, st.Config.Table.URL == "https://gpu.example:8787"
  	// 3. lp, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: ModeLocal}}) → err == nil, one StepConfig,
  	//    lp.Want.Mode == ModeLocal, and lp.Format() names the [model-server] write with mode = "local" and the url it writes;
  	//    pin that url line exactly as today's planLocal produces it (run once, copy the line), so a change to which url wins is visible
  }
  ```
  (Use the fake's accessor names as `fakes_test.go` has them; the step order and the three assertions are what is pinned.)
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/modeleditor -count=1 -run 'TestSetupTab|TestTabBar'` → `undefined: TabSetup`. (`TestModeTabRemoteThenSetupTabLocalAgree` needs no new code: it is a characterisation test and passes at once; run it with `go test ./internal/modelsetup -run TestModeTabRemoteThenSetupTabLocalAgree -count=1`.)
- [ ] **Step 3: Implement** by copying the three server files into `modeleditor` (package clause, `serverX` → `setupX` identifiers, `m.server` → `m.setup`), deleting the remote branches (`visibleFields`' remote case, `serverObserveOptions`' remote case, `serverDesired`'s remote case, `promptSaveAnyway`, `sfURL`/`sfRemoteToken`/`sfPaste`), and wiring the shell as described. `Options.Setup.withDefaults()` fills from `SetupDepsFromEnv(modelsetup.DefaultEnv())` (tabs.go:75-90). In every copied non-test file and its copied tests, replace each `modelclient.Mode*`, `modelclient.ParseMode`, `modelclient.Mode`, `modelclient.Resolution` and `modelclient.Source*` with the same name under `modelmode.` (spine §3.1: modelclient keeps only aliases), and `modelclient.HasUserinfo`/`modelclient.HiddenURL` with `urlrule.HasUserinfo`/`urlrule.Hidden` (today about 17 uses in server_tab.go, 11 in server_view.go — :208-212 and the mode lines :276-465 that Task 22 did not move to modeview — and 1 in server_litellm.go:106); then drop the `modelclient` import (`grep -n 'modelclient' <the copied files>` → no hit). Without this modeleditor links `modelclient`, which is in `sessionBanned`, and `TestModelServerBinaryStaysOffTheSessionPackages` fails once Task 28 wires `setupCmd`.
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/modeleditor ./internal/depcheck -count=1`; `go list -deps ./internal/modeleditor | grep -c internal/modelclient` → `0`.
- [ ] **Step 5: Commit.** `feat(modeleditor): add the Setup tab (local setup, LaunchAgent, LiteLLM) beside Models` — files: `internal/modeleditor/*`, `internal/modelsetup/twoeditors_test.go`.

### Task 28: `wt-model setup`

**Files:**
- Create: `cmd/wt-model/setup.go`, `cmd/wt-model/setup_test.go`
- Modify: `cmd/wt-model/main.go:35`, `cmd/wt-model/model_tui.go` (`runModelEditor` passes `Setup`)

**Interfaces:**
- Consumes: `modeleditor.TabSetup`, `modeleditor.SetupDepsFromEnv` (Task 27); `litellm.DesiredRouteIDsFor` (3a).
- Produces:
  ```go
  func setupCmd() *cobra.Command  // `wt-model setup`: opens the Setup tab; needs a terminal
  // runModelEditor now also passes Setup: modeleditor.SetupDepsFromEnv(setupEnv())
  func setupEnv() modelsetup.Env  // env := modelsetup.DefaultEnv(); env.DesiredRoutes = litellm.DesiredRouteIDsFor(func(c *config.Config) localmodels.Snapshot { return probeInventory(c) }); return env
  ```
  `Use: "setup"`, `Short: "Set up wt-model on this machine (LaunchAgent, token file, LiteLLM key)"`, `Args: cobra.NoArgs`, `SilenceUsage: true`; without a terminal: `errors.New("wt-model setup needs a terminal to open the Setup tab")`. Both `model` and `setup` run the same `runModelEditor` (so the RegistryChanged sync also follows a Models edit made after `setup`).
- [ ] **Step 1: Write the failing tests:**
  - `TestWtModelSetupOpensTheSetupTab`: `call.opts.StartTab == modeleditor.TabSetup`, `call.opts.Setup.Observe != nil`.
  - `TestWtModelSetupGivesTheRoutesCheckTheProbeSeam`: stub `probeInventory` with a marker snapshot; set `WT_LITELLM_CONFIG` to a temp file holding a wt-managed `gone/stale` row and a registry with one openrouter model (`OPENROUTER_API_KEY=sk-test`); call `setupEnv().DesiredRoutes(ctx)` → `[]string{"openrouter/a--b"}` (twin of `TestDesiredRouteIDsAreWhatASyncWouldWrite`, commands_config_test.go:391, which is deleted from cmd/wt-session in Task 31).
  - `TestWtModelSetupWithoutATerminalSaysSo`: error `"wt-model setup needs a terminal to open the Setup tab"`, editor not called.
- [ ] **Step 2: Run, expect FAIL.** `go test ./cmd/wt-model -count=1 -run TestWtModelSetup` → `undefined: setupCmd`.
- [ ] **Step 3: Implement** and add `setupCmd()` to `root.AddCommand`.
- [ ] **Step 4: Run, expect PASS.** `go test ./cmd/wt-model ./internal/depcheck -count=1`.
- [ ] **Step 5: Commit.** `feat(wt-model): add wt-model setup, the Setup tab for this machine` — files: `cmd/wt-model/{setup,setup_test,main,model_tui}.go`.

### Task 29: `internal/sessioneditor` — shell and Agents tab (one tab until Task 30 adds Mode)

**Files:**
- Create (copied from configeditor, then edited): `internal/sessioneditor/agents_tab.go`, `agent_form.go`, `delete.go`, `save.go`, `filter.go`, `editor.go` (shell), `tabs.go`; tests `agents_tab_test.go`, `agent_form_test.go`, `delete_test.go`, `save_test.go`, `editor_test.go`, `quit_test.go`, `helpers_test.go`, `testmain_test.go`
- Modify: `internal/depcheck/depcheck_test.go` (sessionLibraries, sessionBanned, adminScreens)

**Interfaces:**
- Consumes: `config.ValidateSession` (3a), `editorkit.*`.
- Produces (spine §6, the parts this task needs):
  ```go
  type Tab int
  const (TabAgents Tab = iota; TabMode)     // TabMode is drawn and reachable from Task 30
  var tabNames = []string{"Agents"}         // Task 30 makes it {"Agents", "Mode"}
  type Options struct{ StartTab Tab; LookPath func(string) (string, error); UserHome func() (string, error) } // Mode added in Task 30
  type Result struct{ RunModelSetup bool; ModelSetup string }
  func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error)
  ```
  `handleSave` validates with `m.cfg.ValidateSession()`; its status is `"validation: " + err.Error()` with no registry hint (no registry is read). `loadedMsg`'s banner is editor.go:202-210 without the `config.RegistryFixHint` suffix: `"config load/validation error: " + err.Error()` (a `LoadSession` error is config.toml's).

- [ ] **Step 1: Write the failing tests.** Copy unchanged (package clause only): all of `agent_form_test.go`, `agents_tab_test.go`, `delete_test.go`, `quit_test.go` (incl. the tests Task 24 moved there), `editor_test.go` minus `TestLoadedMsgLocationErrorNamesTheRegistry` (it went to modeleditor in Task 24), and `save_test.go` minus `TestSave_RegistryRowErrorNamesTheRegistry` and `TestSave_JoinedErrors_RegistryHintPreferred` (registry rows are not validated here; they are deleted with configeditor in Task 31). New in `save_test.go`:
  ```go
  func TestSave_ValidatesOnlyTheSessionRules(t *testing.T) {
  	// agent naming provider "nosuch" with no providers loaded: the save is dispatched (m.saving == true)
  	// two agents named "a": status == `validation: duplicate agent name "a"`, m.saving == false
  }
  func TestSaveNeverWritesASynthesizedAgent(t *testing.T) {
  	// isolated config home; config.toml with a codex agent; m.cfg from config.LoadSession("claude"); dirty; ctrl+s through the real saveCmd
  	// file afterwards contains `name = "codex"` and not `name = "claude"`
  }
  ```
  `depcheck_test.go`: add `"internal/sessioneditor"` to `sessionLibraries` and to `sessionBanned`, and to `adminScreens`; `TestModelServerBinaryStaysOffTheSessionPackages` then also proves wt-model never links it.
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/sessioneditor ./internal/depcheck -count=1` → no Go files / `internal/sessioneditor is named … but no longer exists`.
- [ ] **Step 3: Implement** by copying the agents files and trimming `configeditor/editor.go` to the Agents tab (no `server` field, no `TabServer`; `leave()` returns `tea.Quit`; no `discardHeld` yet — Task 30 adds it with the first write that can hold a quit). `Run` resolves `LookPath` (default `exec.LookPath`) and `UserHome` once.
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/sessioneditor ./internal/depcheck -count=1`.
- [ ] **Step 5: Commit.** `feat(wt): add internal/sessioneditor with the Agents tab, saving through ValidateSession` — files: `internal/sessioneditor/*`, `internal/depcheck/depcheck_test.go`.

### Task 30: the Mode tab (native, remote, local pointer) and the offer to run `wt-model setup`

**Files:**
- Create: `internal/sessioneditor/mode_tab.go`, `mode_view.go`, `mode_tab_test.go`, `mode_aturl_test.go`
- Modify: `internal/sessioneditor/editor.go`, `tabs.go`

**Interfaces:**
- Consumes: `modelmode.Observe/BuildPlan/Apply/DefaultEnv/State/Desired/Plan/Result/ObserveOptions/Secret` (1c); `modeview.*` (3a); `editorkit.PromptView/PlanPromptView/FitBody/Groups/ChoiceLines/RenderLine`.
- Produces (spine §6):
  ```go
  var tabNames = []string{"Agents", "Mode"}
  type ModeDeps struct{ Observe func(ctx context.Context, o modelmode.ObserveOptions) modelmode.State
  	Plan func(st modelmode.State, want modelmode.Desired) (modelmode.Plan, error)
  	Apply func(ctx context.Context, p modelmode.Plan, progress func(string)) modelmode.Result }
  func ModeDepsFromEnv(env modelmode.Env) ModeDeps   // Observe → modelmode.Observe(ctx, env, o); Plan → modelmode.BuildPlan; Apply → modelmode.Apply(ctx, env, p, progress)
  type Options struct{ StartTab Tab; Mode ModeDeps; LookPath func(string) (string, error); UserHome func() (string, error) }
  var modeChoices = []modelmode.Mode{modelmode.ModeNative, modelmode.ModeLocal, modelmode.ModeRemote} // all allowed on every platform
  // phases: modeView, modeEdit, modeConfirm, modeApplying; prompts: promptApply, promptSaveAnyway, promptRunSetup
  const localPointerNote = "run `wt-model setup` to set up wt-model on this machine"
  const runSetupQuestion = "wt-model is on PATH: run `wt-model setup` now? [y/N]"
  ```
  Fields: remote only (`url`, `token`, `paste`; placeholders `"https://host:8787"`, `"path of a 0600 token file"`, `"or paste the token (hidden)"`); native and local show none. Keys on the tab: `left/right/space` mode, `down`/`enter` fields, `a` apply, `c` check, `r` re-read, `tab` next, `q`/`ctrl+c` quit; no `k`, `t`, `s`, `x`, `R` (they do nothing; hints never name them). Status block lines, in order: `ModeLine`, `URLLine`, `TokenLine`, `ServerCheckLine`, `EnvLine`, `ProblemLines` (no pair, agent, LiteLLM or plist rows). Held quit: `leave()` waits while `m.mode.writing`, on `TabMode`; `discardHeld` as configeditor's. After a successful apply whose plan's `Want.Mode == ModeLocal`: if `o.LookPath("wt-model")` returns a path, the confirm phase opens with `promptRunSetup`; `y` sets `m.result = Result{RunModelSetup: true, ModelSetup: path}` and quits through `leave()` (the agents prompt still asks first when dirty); `n`/`esc` stays on the tab with the status `localPointerNote`. Without `wt-model` on PATH the status is `localPointerNote` and no question.

- [ ] **Step 1: Write the failing tests** in `mode_tab_test.go` (machine `modeMachine{states []modelmode.State; plan modelmode.Plan; build bool; result modelmode.Result; …}` with `deps() ModeDeps`, trimmed from `serverMachine`, server_tab_test.go:60-134). Moved by rename from configeditor (`TestServerTab*` → `TestModeTab*`, assertions unchanged except where noted): `…ObservesOnFirstVisitOnly`, `…ShowsTheEffectiveModeAndEnvOverride`, `…ShowsAParseErrorWithThePath`, `…DropsAStaleObservation`, `…FitsTheTerminal`, `…QuitKeysUseTheExistingQuit`, `…ViewIsASCII`, `…NeverShowsASecret`, `…AlwaysReObservesBeforePlanning`, `…RemotePingsTheEnteredURLBeforePlanning` (asserts `ObserveOptions.CheckURL`, `CheckToken`, `CheckTokenFile` on `modelmode.ObserveOptions`), `…DisablesWritesOnAParseError`, `…ConfirmIsDefaultNo`, `…AppliesAfterY`, `…RefusalIsShownVerbatim`, `…OffersSaveAnywayDefaultNoAfterAFailedRemoteCheck`, `…QuitWaitsForAnApplyInFlight`, `…MasksThePastedTokenAndTheKey` (pasted-token half), `…FitsTheTerminalInEveryPhase` (native/remote/local phases), `…ConfirmSaysTheURLsCredentialsAreNotKept`, `…RealPlanNeverShowsTheSecrets` (`build: true` → `modelmode.BuildPlan`), `…FormIsInertWhileAPlanIsPrepared`, `…PlansTheSnapshotTakenAtA`, `…ScrubsTheResolutionProblem`, `…HintsNameOnlyKeysThatAct`, `…CallsApplyAndControlOnlyFromCommands` (Apply only), `…SourceNeverFormatsAPlanWithV` (scans `mode_*.go`), `…NeverRendersAPieceOfAnAtURL` (remote/native/stored/entered/env surfaces); `TestStartTabOpensTheModelServerTab` → `TestStartTabOpensTheModeTab` (view contains `"[Mode]"`; `tab` goes to Agents); `TestCtrlSOnTheServerTabDoesNothing` → `TestCtrlSOnTheModeTabDoesNothing`; from polish: `…CancelsAPlanWhenCOrRReadsAgain`, `…CancelsAPlanInFlightWhenCOrRReadsAgain`, `…NamesWhatACheckIsMissing`, `…PromptsStayVisibleAtEverySize`, `…ClippedViewKeepsRadiosHintsAndWholeRows`; `mode_aturl_test.go` ← `TestServerTabNeverPingsAStoredCredentialedURL` (fakes typed on `modelmode.FS/ConfigStore/Net/Host`). New:
  ```go
  func TestModeTabOffersAllThreeModesEverywhere(t *testing.T) // off-macOS-like state: modeChoice radios are native, local, remote; right cycles through all three
  func TestModeTabShowsNoAdminRows(t *testing.T)           // plain view has none of "pair", "agent", "litellm", "plist", "listen", "master"
  func TestModeTabAdminKeysAreInert(t *testing.T)           // k, t, s, x, R: no dep call (seq unchanged), phase stays modeView
  func TestModeTabLocalPlansThePointer(t *testing.T)         // choose local, a → Plan was given Desired{Mode: local} with URL, TokenFile, PastedToken empty; with build:true the confirm view holds p.Format() of modelmode.BuildPlan(st, Desired{Mode: local}) for the same State (the text 1c pins in modelmode/testdata/plan_local_pointer.golden)
  func TestModeTabRemoteToLocalNeverKeepsTheRemoteURL(t *testing.T) {
  	// stored remote: Table{Mode: "remote", URL: "https://gpu.example:8787", TokenFile: "/y/t"}, Resolution{ModeRemote, SourceStored}; build:true
  	// on remote, type into url "https://other.example:1"; switch the radio to local → the edit view shows no url/token/paste field
  	//   (visibleFields() is empty) and no line of the form area contains "gpu.example" or "other.example"
  	// a → Plan was given Desired{Mode: local} with URL == "" and TokenFile == "" (the typed url is dropped);
  	// the confirm view contains `url = "http://` + modelmode.DefaultListen + `"` and modelmode.DefaultLocalTokenPath(configHome),
  	// and contains neither "gpu.example" nor "/y/t" nor "other.example"; y → the applied plan's Want.URL == "http://" + modelmode.DefaultListen
  }
  func TestModeTabOffersToRunWtModelSetupWhenOnPath(t *testing.T) {
  	// LookPath returns "/Users/user_name/.local/bin/wt-model"; local applied OK → view contains "wt-model is on PATH: run `wt-model setup` now? [y/N]"
  	// "y" → cmd is tea.Quit and m.result == Result{RunModelSetup: true, ModelSetup: "/Users/user_name/.local/bin/wt-model"}
  }
  func TestModeTabRunSetupIsDefaultNo(t *testing.T)         // "n", "esc", "enter" → no quit, result zero, status == "run `wt-model setup` to set up wt-model on this machine"
  func TestModeTabWithoutWtModelOnPathOnlyPoints(t *testing.T) // LookPath error → no question; status is the pointer note
  func TestModeTabLeavingLocalNotesTheAgentIsUntouched(t *testing.T) // stored local → native with build:true: plan text contains "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`"; no remove-agent prompt
  func TestTabBarShowsAgentsAndMode(t *testing.T)           // tabBar(theme, TabMode) contains "Agents" and "[Mode]"
  ```
- [ ] **Step 2: Run, expect FAIL.** `go test ./internal/sessioneditor -count=1 -run 'TestModeTab|TestStartTabOpensTheModeTab|TestTabBarShowsAgentsAndMode'` → `undefined: ModeDeps`.
- [ ] **Step 3: Implement** `mode_tab.go` from `configeditor/server_tab.go` (phases, generations, observe/plan/apply commands, form) with the admin parts removed (Control, LiteLLM menu, listen/master fields, `promptRemoveAgent`, the probe-read logic of server_tab.go:735-744), and `mode_view.go` from `server_view.go` (hints as server_view.go:23-82 minus `k`/`t`/`s/x/R`; body through `editorkit.FitBody` with drop order unchanged minus the admin groups). `observeOptions()` = server_tab.go:265-296's remote case only on `modelmode.ObserveOptions`; `desired()` = :298-316's remote case on `modelmode.Desired`. `ModeDeps.withDefaults()` fills from `ModeDepsFromEnv(modelmode.DefaultEnv())`. In every copied non-test file and its copied tests, replace each `modelclient.Mode*`, `modelclient.ParseMode`, `modelclient.Mode`, `modelclient.Resolution` and `modelclient.Source*` with the same name under `modelmode.` (spine §3.1: modelclient keeps only aliases), and `modelclient.HasUserinfo`/`modelclient.HiddenURL` with `urlrule.HasUserinfo`/`urlrule.Hidden` (count only the two files this task copies: on f60ad48a `server_tab.go` has 17 lines (21 uses) and `server_view.go` 11 lines; after Task 22 moved `modeSource` (:148-156) and `serverCheckLine`/`missingForCheck` (:276, :293) to modeview, `server_view.go` keeps :208, :212 (`pairLine`), :323 (`litellmLines`) and :465 (`modeChoice`), and the admin parts removed above take `pairLine`, `litellmLines` and server_tab.go's Control/LiteLLM uses with them, so recount on the copies rather than trusting these numbers); then drop the `modelclient` import (`grep -n 'modelclient' internal/sessioneditor/mode_tab.go internal/sessioneditor/mode_view.go internal/sessioneditor/mode_*_test.go` → no hit).
- [ ] **Step 4: Run, expect PASS.** `go test ./internal/sessioneditor ./internal/depcheck -count=1` (sessioneditor's closure must not contain `internal/modelsetup`, `internal/modelclient` or `internal/tui`); `go list -deps ./internal/sessioneditor | grep -c internal/modelclient` → `0`.
- [ ] **Step 5: Commit.** `feat(sessioneditor): add the Mode tab (native, remote, local pointer) and the offer to run wt-model setup` — files: `internal/sessioneditor/*`.

### Task 31: `wt config` and the first-run gate open `sessioneditor`; `configeditor` deleted

**Files:**
- Modify: `cmd/wt-session/commands_config.go:1-80`, `cmd/wt-session/models.go:121-150` (ensureMode), `:210-226` (ping hints), `cmd/wt-session/main.go:477-486` (comments), `internal/modelmode/mode.go` (ModeUnsetError text; was modelclient/mode.go:81), `internal/modelclient/endpoint.go:40` (NotConfiguredError tail), `cmd/wt-session/mode_gate_test.go`, `cmd/wt-session/commands_config_test.go`, `cmd/wt-session/main_test.go:24-40`, `cmd/wt-session/models_test.go:755-770`, `internal/modelmode/mode_test.go:97`, `internal/modelclient/endpoint_test.go:135`, `internal/depcheck/depcheck_test.go`, `cmd/wt-session/litellm.go` (delete the `desiredRouteIDs` wrapper, Step 3), `cmd/wt-session/model_test.go` (`TestBareWtModelPointsAtWtModel`, added in Task 24: change its `stubConfigEditor(t, configeditor.Result{}, nil)` to `stubConfigEditor(t, sessioneditor.Result{}, nil)` (the helper Step 1 retypes) and drop the `configeditor` import; the assertion `call.called == false` stays), `wt/Makefile:89` (`single-p`: delete `./internal/configeditor` and add `./internal/sessioneditor ./internal/modeleditor` beside it; without this `go test ./internal/configeditor` fails "directory not found" in wt-ci's single-p job and in `make test-all` from this PR on)
- Delete: `internal/configeditor/` (whole directory)
- Create: `cmd/wt-session/modesetup.go`, `cmd/wt-session/modesetup_test.go`

**Interfaces:**
- Consumes: `sessioneditor.Run/Options/ModeDepsFromEnv/Result/TabAgents/TabMode` (Task 29–4); `modelmode.DefaultEnv` (1b).
- Produces:
  ```go
  // commands_config.go — renamed seam (was configeditorRun, commands_config.go:29)
  var sessioneditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error, o sessioneditor.Options) (sessioneditor.Result, error) { return sessioneditor.Run(theme, cfg, cfgErr, o) }
  // runConfigEditor keeps its role, now session-only. The load and the error it hands the editor:
  //   cfg, err := config.LoadSession(); cfgErr := err; if err == nil { cfgErr = cfg.ValidateSession() }
  // A parse error leaves cfg nil: the editor builds its own stand-in, as it does today for a nil cfg, and disables
  // writes; a validation error (duplicate agents, a default provider outside its list) shows the banner on open,
  // as today's a.hostConfigErr() did. No registry is read. It runs sessioneditorRun with
  // Options{StartTab: start, Mode: sessioneditor.ModeDepsFromEnv(modelmode.DefaultEnv())}; then, when res.RunModelSetup,
  // runModelSetupChild(res.ModelSetup). Signature:
  func runConfigEditor(a *app, start sessioneditor.Tab) error
  // modesetup.go
  var runModelSetupChild = func(path string) error // exec.Command(path, "setup") with os.Stdin/Stdout/Stderr; returns the child's error
  ```
  `ensureMode` (models.go:129) calls `runConfigEditor(a, sessioneditor.TabMode)`; a `runModelSetupChild` failure inside the gate is printed as one line `wt: warning: wt-model setup did not finish: <err>` on stderr and the gate goes on (the mode is already stored); in `wt config` it is returned (`wt-model setup: <err>`, exit 1). Strings (spine §15.21): ModeUnsetError → `"no model server is configured — choose a mode: run wt config (Mode tab) or set WT_MODEL_MODE to native, local or remote"`; NotConfiguredError tail → `"… or choose the mode in wt config (Mode tab): native, local or remote"`; models.go:223 → `"local mode: start it with wt-model setup or run launchctl kickstart -k gui/%d/local.wt-model.serve"`; models.go:225 → `"… or change the url or mode in wt config (Mode tab)"`. `wt config` Long:
  ```
  "Manage wt user preferences.\n\n" +
  "With no subcommand, launches an interactive TUI with two tabs (tab\n" +
  "switches): Agents, the agents in config.toml, saved with ctrl+s; and Mode,\n" +
  "how sessions reach a model server: native, remote (its url and token are\n" +
  "checked here) or local (set up this machine with `wt-model setup`). The\n" +
  "models in registry.toml are managed with `wt-model model`.\n\n" +
  "Subcommands:\n  theme   active color theme\n  path    print the config directory"
  ```
  `depcheck_test.go`: delete `"internal/configeditor"` from `adminBanned`, `sessionBanned`, `adminScreens`; the comments at lines 12-15, 31-35 name sessioneditor/modeleditor.

- [ ] **Step 1: Write/adjust the failing tests.**
  - `mode_gate_test.go`: `stubEditorSideEffect` returns `(runs *int, tab *sessioneditor.Tab)` over `sessioneditorRun`; `TestGateOpensTheModelServerTabOnATerminal` → `TestGateOpensTheModeTabOnATerminal` (`*tab == sessioneditor.TabMode`); the rest keep their names and assertions with the seam type changed. New:
    ```go
    func TestGateRunsWtModelSetupWhenTheTabAsks(t *testing.T) {
    	// editor stores mode = "local" and returns Result{RunModelSetup: true, ModelSetup: "/x/wt-model"}; runModelSetupChild stubbed to record its path
    	// → the child ran once with "/x/wt-model", after the editor, before the second buildModels
    }
    func TestGateGoesOnWhenWtModelSetupFails(t *testing.T) // child returns errors.New("exit status 2") → stderr has "wt: warning: wt-model setup did not finish: exit status 2"; the launch then takes local mode's own handling (no second editor run)
    ```
  - `commands_config_test.go`: `stubConfigEditor` over `sessioneditorRun` (`editorCall.opts sessioneditor.Options`); `TestConfigEditorIsGivenTheModelServerDeps` → `TestConfigEditorIsGivenTheModeDeps` (`call.opts.Mode.Observe != nil`); `TestConfigLongTextNamesTwoTabsAndTheModelsMove` → `TestConfigLongTextNamesAgentsAndMode` (contains `"two tabs"`, `"Agents"`, `"Mode"`, `"wt-model setup"`, `"wt-model model"`; not `"Model server"`); `TestDesiredRouteIDsAreWhatASyncWouldWrite` deleted (twin in Task 28); `TestWtConfigOpensWhenConfigTomlIsSyntaxBroken` keeps its assertions (the error now comes from `LoadSession`); `TestConfigCmd_InvalidConfig_LaunchesEditor` (:108-126) stops using `setHostCfgErr` (the host config is no longer `wt config`'s input) and becomes `TestWtConfigShowsSessionValidationOnOpen`: a config.toml whose `[[agents]]` holds `name = "claude"` twice (each with `supported_providers = ["ollama"]`) → the editor runs, `call.cfg != nil`, `call.cfgErr` contains `duplicate agent name "claude"`; new `TestWtConfigReadsNoRegistry`: a registry file that is garbage (`"not toml [["`) → `wt config` opens the editor with `cfgErr == nil` and the registry bytes unchanged; new `TestWtConfigRunsWtModelSetupWhenAsked` (Result `RunModelSetup` → child ran with that path; a child error → `wt config` error text `"wt-model setup: exit status 2"`).
  - `main_test.go:26` `TestConfig_NoSubcommand_LaunchesEditor`: seam type only.
  - `models_test.go:767`: wants `"wt-model setup"` instead of `"wt config (Model server tab)"`.
  - `internal/modelmode/mode_test.go:97`, `internal/modelclient/endpoint_test.go:135`: the `(Mode tab)` texts above.
- [ ] **Step 2: Run, expect FAIL.** `go test ./cmd/wt-session -count=1 -run 'Gate|Config|OneShot|SoftOneShot|BareWt|ResetModelCaches|NotReachable|LocalMode'` → `undefined: sessioneditorRun`; `go test ./internal/modelmode ./internal/modelclient -count=1 -run 'ModeUnset|NotConfigured'` → text mismatch.
- [ ] **Step 3: Implement** as above; `git rm -r internal/configeditor`; remove the `configeditor` and `modelsetup` imports from `cmd/wt-session` (`commands_config.go`, `models.go`, `model.go`); delete the `desiredRouteIDs` wrapper and its comment from `cmd/wt-session/litellm.go` (Task 19 kept it for `commands_config.go:46`, its last caller, deleted here; `TestDesiredRouteIDsAreWhatASyncWouldWrite` goes in Step 1), so it no longer reads `probeInventory` (`grep -n desiredRouteIDs cmd/wt-session/*.go` → no hit); `grep -rn configeditor --include='*.go' .` → no hit.
- [ ] **Step 4: Run, expect PASS.** `go build ./... && go vet ./... && gofmt -l .`; `go test ./internal/depcheck ./internal/sessioneditor ./internal/modeleditor ./internal/modelmode ./internal/modelclient ./cmd/wt-model -count=1`; the cmd subset from Step 2 plus `-run 'NoWtCommandIsHidden|RootHelp|Model'`; `go list -deps ./cmd/wt-session | grep -E 'internal/(configeditor|modelsetup|modeleditor)$'` → no output; `grep -c configeditor Makefile` → 0; `make single-p` (background, exit captured).
- [ ] **Step 5: Commit.** `feat(wt): wt config opens the session editor (Agents, Mode); the first-run gate opens the Mode tab; delete configeditor` — files: every file named in **Files** (`wt/Makefile` and `cmd/wt-session/model_test.go` included).

### Task 32: docs for 3c

**Files:** `docs/wt-config.md` (two tabs: Agents and Mode; the "Model server tab" section becomes "Mode tab": native, remote, local pointer, the `[y/N]` offer; local setup → `wt-model setup`), `docs/wt-model-server.md:31-32,129` ("The way in is `wt-model setup`"; a `wt-model setup` section: Setup tab, local and native only, remote is set in `wt config`), `docs/configuration.md` (`[model-server]` written by the Mode tab or `wt-model setup`), `docs/wt-session.md` (first-run gate → Mode tab), `docs/internals/{tui,launch-flow,config-and-registry,packages}.md` (configeditor rows replaced by sessioneditor/modeleditor; `ensureMode` → Mode tab), `CLAUDE.md` (wt: "LiteLLM routing state" first-run gate sentence, "Config (Go)" `wt config` Model server tab → Mode tab / `wt-model setup`, the package list in "Go module" (`configeditor` → `sessioneditor,modeleditor,editorkit,modeview`), "Import graph ratchet", "wt-model" bullet adds `setup`), `../CLAUDE.md` (the `wt/` architecture bullet), `../docs/guides/00-config-map.md`, `01-initial-setup.md`, `13-deployment-use-cases.md`, `14-litellm-install-and-configuration.md` (every "Model server tab").

- [ ] **Step 1:** `grep -rn -e 'Model server tab' -e 'configeditor' -e 'three tabs' docs ../docs/guides CLAUDE.md ../CLAUDE.md internal cmd` (docs under `docs/superpowers/` are dated records: left as written).
- [ ] **Step 2:** edit each hit outside `superpowers/` to the 3c state (no live model state; `/Users/user_name` for home paths).
- [ ] **Step 3:** `make -C .. check-links` → exit 0.
- [ ] **Step 4:** re-run the Step 1 grep restricted to non-superpowers paths → no hit except historical notes that say "was".
- [ ] **Step 5: Commit.** `docs(wt): wt config's Mode tab and wt-model setup replace the Model server tab` — files: the edited docs.

---

## PR 4a: internal/cli extraction, the pointer stub family, and model init|list|add|edit|rm + served + warm move to wt-model

The spine (§13) ships this stage as **three PRs**, planned here in order: **4a-i** (`internal/cli`, `moved.go`, the table test), **4a-ii** (`model init|list|add|edit|rm` + modeltiers + the scripts, hint and skills that call them), **4a-iii** (`served`, `warm` + llmbench). Each has its own Entry/Exit block below; the line block here is the whole stage.

- **Entry:** PR 3c merged (sessioneditor and modeleditor exist, `configeditor` deleted, `cmd/wt-model` has `host.go` (`type host struct`, 3b), the bare `wt-model model` TUI command and `wt-model setup`; bare `wt model` already prints the pointer; `internal/litellm` has `SyncAfterWrite`/`SyncAndWarn`/`Probe` (3a)).
- **Exit gate (each of the three PRs):** `cd wt && gofmt -l . | wc -l` prints 0; `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (full `cmd/wt-session` run, about 3.5 minutes); `cd wt && go test ./internal/depcheck ./internal/removal -count=1`; `make lint-skills && bin/check-links` (repo root); 4a-ii adds `cd modeltiers && make check && make test`; 4a-iii adds `cd llmbench && make check && make test`.
- **Out of scope here:** `wt stop <provider>|--all` (4d-ii; so `bin/update/rec`, `bin/update/sushi` and `mtplx-profile-matrix`'s `wt stop mtplx` keep `wt stop`), `cloud-sync` (4b), `litellm sync|list|providers|restart` (4c), the `seen < 20` floors (5a, spine §14).

Conventions for every task: Go commands run from `wt/`; every new `Test*` has a top-level `//` comment saying what it pins and why; a moved test keeps its name and comment (only command names in the comment change); no test, golden or doc holds a real home path (`/Users/user_name`) or a secret.

---

### PR 4a-i: `internal/cli` and the pointer stub family

- **Entry:** 3c merged (above). **Exit:** the gate above; `go list -deps ./internal/cli | grep ohanaverse` prints only `wt/internal/cli` and `wt/internal/exitcode`.

### Task 33: `internal/cli` — approval refusals and terminal questions

**Files:**
- Create: `wt/internal/cli/approval.go`, `wt/internal/cli/prompt.go`, `wt/internal/cli/terminal.go`
- Create: `wt/internal/cli/approval_test.go`, `wt/internal/cli/prompt_test.go`
- Modify: `wt/internal/depcheck/depcheck_test.go` (`sessionLibraries` :45; the `TestModeLeavesStayOffTheClient` package list added in 1a)
- Modify: `wt/CLAUDE.md` ("Import graph ratchet" paragraph: one clause naming `internal/cli` as a session library leaf)

**Interfaces:**
- Consumes: `exitcode.New(code int, err error, quiet bool) error` (`internal/exitcode`).
- Produces (spine §8, exact):
  ```go
  var ErrNoTerminal = errors.New("there is no terminal to ask")
  const ExitApprovalMissing = 3
  func ApprovalMissing(flag, state string) error            // exitcode.go:55 body
  func NoTerminalRefusal(err error, flag, state string) error // exitcode.go:61
  func Declined(state string) error                         // exitcode.go:71
  func ReadYesNo(r io.Reader, def bool) (bool, error)       // launch.go:64 askYesNoDefault body
  func AskOnTTY(open func() (*os.File, error), question string, noTTY error) (bool, error)
  func IsStdinTTY() bool                                    // helpers.go:128
  func StdoutWidth() int                                    // stats_usage.go:244 realStdoutWidth
  ```
  `AskOnTTY`: `f, err := open()`; on error return `false, noTTY`; `defer f.Close()`; write `question + " [y/N] "` to `f`; return `ReadYesNo(f, false)` (the shared body of `promptStop` model_cmds.go:58 and `promptCloudSync` cloudsync.go:107).

- [ ] **Step 1: Write the failing tests.**
  - `approval_test.go` `TestApprovalMissingWording` (moved twin of `cmd/wt-session/approval_missing_test.go:35`; that one stays until Task 35): `ApprovalMissing("--yes", "nothing was stopped").Error() == "approval missing: pass --yes (there is no terminal to ask); nothing was stopped"`, `exitcode.Of(err) == 3`; `Declined("nothing was removed").Error() == "approval missing: declined; nothing was removed"`, code 3; `NoTerminalRefusal(fmt.Errorf("x: %w", ErrNoTerminal), "--yes", "s")` equals `ApprovalMissing("--yes","s")` by `Error()`; `NoTerminalRefusal(errors.New("boom"), …).Error() == "boom"`. (Use the exit-code accessor `exitcode` exports today: `go doc ./internal/exitcode`.)
  - `prompt_test.go` `TestReadYesNoAppliesTheDefault`: table `{"y\n",false→true}`, `{"yes\n",false→true}`, `{"Y\n",false→true}`, `{"n\n",true→false}`, `{"no\n",true→false}`, `{"\n",false→false}`, `{"\n",true→true}`, `{"maybe\n",true→true}`, `{"" (EOF),true→true}`, `{"" (EOF),false→false}`; every error nil.
  - `prompt_test.go` `TestAskOnTTYWithoutATerminalReturnsTheCallersError`: `open` returns `errors.New("open /dev/tty: device not configured")`; `noTTY := errors.New("rerun with --yes")`; got `(false, noTTY)` (`errors.Is`).
  - `prompt_test.go` `TestAskOnTTYAsksOnTheFileItOpened`: the socketpair pattern of `cmd/wt-session/cloudsync_test.go:497-512` (a temp file cannot stand in: `AskOnTTY` writes the question first, which moves an `O_APPEND` offset to EOF, so the read sees EOF). `fds, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)`; `tty, user := os.NewFile(uintptr(fds[0]), "tty"), os.NewFile(uintptr(fds[1]), "user")`; `open` returns `tty`; a goroutine reads the question from `user`, writes `"y\n"`, closes `user` and sends what it read on a channel; question `"Stop it?"`; got `(true, nil)` and the question read is exactly `"Stop it? [y/N] "`.
  - `depcheck_test.go`: add `"internal/cli"` to `sessionLibraries` and to the leaf list `TestModeLeavesStayOffTheClient` walks.
- [ ] **Step 2: Run, expect FAIL** (package does not exist): `cd wt && go test ./internal/cli ./internal/depcheck -count=1` → build failure `no non-test Go files in …/internal/cli` / depcheck `package internal/cli not found`.
- [ ] **Step 3: Implement** the seven names above in the three files (approval.go: the error trio + const; prompt.go: `ReadYesNo`, `AskOnTTY`; terminal.go: `IsStdinTTY` via `term.IsTerminal(os.Stdin.Fd())`, `StdoutWidth` via `term.GetSize(os.Stdout.Fd())`, 0 on error). Doc comments carry the reasons the originals give (stdin is the interactive signal; piped input never answers a y/N). Package comment: "Package cli holds the command-line helpers both wt binaries use: approval refusals, y/N questions on the terminal, the plain table. A session-safe leaf: it imports exitcode and the terminal libraries only."
- [ ] **Step 4: Run, expect PASS:** `cd wt && go test ./internal/cli ./internal/depcheck -count=1 && go vet ./internal/cli`.
- [ ] **Step 5: Commit** `internal/cli/*`, `internal/depcheck/depcheck_test.go`, `wt/CLAUDE.md`:
  `feat(cli): add the shared command-line leaf with approval refusals and terminal questions`

### Task 34: `internal/cli` — the plain table, `Dash`, `VisibleID`

**Files:**
- Move: `git mv wt/cmd/wt-session/plain_table.go wt/internal/cli/plaintable.go` (then edit: package `cli`, exported names)
- Create: `wt/internal/cli/text.go` (`Dash` from `cmd/wt-session/model_list.go:247`; `VisibleID` + `invisible` from `cmd/wt-session/stats_usage_table.go:47-68`)
- Create: `wt/internal/cli/plaintable_test.go`, `wt/internal/cli/text_test.go`
- Modify: `wt/cmd/wt-session/model_list.go`, `model_list_server.go`, `stats_usage_table.go`, `profile_slots_manage.go` (every `plainColumn{head:,right:}` → `cli.PlainColumn{Head:,Right:}`, `renderPlainTable` → `cli.RenderPlainTable`, `plainTableWidth` → `cli.PlainTableWidth`, `dash` → `cli.Dash`, `visibleID` → `cli.VisibleID`); delete `dash` (model_list.go:247-252) and `visibleID`/`invisible` (stats_usage_table.go:47-68)
- Modify (tests): `wt/cmd/wt-session/model_list_test.go` (`TestPlainTableAlignsBothKindsOfColumn` moves out), `wt/cmd/wt-session/stats_usage_table_test.go` (`TestVisibleIDEscapesInvisibleCharacters` :152 moves out)

**Interfaces:**
- Produces (spine §8 + one addition, `VisibleID`, which the spine does not list):
  ```go
  type PlainColumn struct{ Head string; Right bool }
  func PlainTableWidth(cols []PlainColumn, rows [][]string) (key, rest int)
  func RenderPlainTable(cols []PlainColumn, rows [][]string, width int) string
  func Dash(s string) string
  func VisibleID(id string) string   // not in spine §8; needed by wt-model's model list (Task 37) and by four wt-session files
  ```
  `plainGap`, `plainWidths`, `plainRest` stay unexported in `plaintable.go`.

- [ ] **Step 1: Write the failing tests.** Move `TestPlainTableAlignsBothKindsOfColumn` from `cmd/wt-session/model_list_test.go` to `internal/cli/plaintable_test.go` unchanged except the names (`cli.PlainColumn`, `RenderPlainTable`, `PlainTableWidth`); move `TestVisibleIDEscapesInvisibleCharacters` to `internal/cli/text_test.go` (calls `VisibleID`); add `TestDashStandsInForAnEmptyCell`: `Dash("") == "-"`, `Dash("x") == "x"`, `Dash(" ") == " "`.
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./internal/cli -run 'PlainTable|VisibleID|Dash' -count=1` → `undefined: PlainColumn` / `RenderPlainTable` / `VisibleID` / `Dash`.
- [ ] **Step 3: Implement**: the `git mv` and renames above; `text.go` holds `Dash`, `VisibleID`, `invisible` with their comments. The file comment of plaintable.go becomes "The borderless, width-aware text table `wt stats`, `wt model list --server` and `wt-model model list` print."
- [ ] **Step 4: Run, expect PASS:** `cd wt && go build ./... && go vet ./... && go test ./internal/cli -count=1 && go test ./cmd/wt-session -run 'ModelList|Stats|ProfileSlots|VisibleID' -count=1`.
- [ ] **Step 5: Commit** (moved file, the new files, the four call-site files, both test files):
  `refactor(cli): move the plain table, Dash and VisibleID to internal/cli`

### Task 35: `cmd/wt-session` uses `internal/cli` for refusals, questions and the terminal

**Files:**
- Modify: `wt/cmd/wt-session/exitcode.go:42-73` (delete `exitApprovalMissing`, `errNoTerminal`, `approvalMissing`, `noTerminalRefusal`, `declined`; keep `agentExitStatus` :19 and `reportExit` :35)
- Modify: `wt/cmd/wt-session/launch.go:64` (delete `askYesNoDefault`; `promptProfile` calls `cli.ReadYesNo(f, true)`; `openTTY` :50 stays as this binary's seam)
- Modify: `wt/cmd/wt-session/start.go:44-62` (`promptReplace` = `cli.AskOnTTY(func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }, question, cli.ErrNoTerminal)`; `askYesNo(r)` = `cli.ReadYesNo(r, false)`, kept as the name the start/answerer code and `TestAskYesNoDefaultsToNo` use)
- Modify: `wt/cmd/wt-session/model_cmds.go:58` (`promptStop(q) = cli.AskOnTTY(openTTY, q, cli.ErrNoTerminal)`)
- Modify: `wt/cmd/wt-session/cloudsync.go:107` (`promptCloudSync(q) = cli.AskOnTTY(openTTY, q, errors.New("there is no terminal to confirm on — rerun with --yes to apply without asking"))`)
- Modify: `wt/cmd/wt-session/helpers.go:128-134` (delete `isStdinTTY`; `var stdinTTY = cli.IsStdinTTY`; the rationale comment moves onto `cli.IsStdinTTY`)
- Modify: `wt/cmd/wt-session/stats_usage.go:242-250` (`var stdoutWidth = cli.StdoutWidth`; delete `realStdoutWidth`)
- Modify: every caller of the deleted names — find them with `cd wt && grep -n 'errNoTerminal\|approvalMissing(\|noTerminalRefusal(\|declined(\|exitApprovalMissing\|askYesNoDefault\|isStdinTTY\|realStdoutWidth' cmd/wt-session/*.go` (today: answerer.go, model_cmds_remote.go, launch_remote.go, start.go, model_cmds.go, model_write.go, launchwarn.go, helpers_test.go) and rename to `cli.ErrNoTerminal`, `cli.ApprovalMissing`, `cli.NoTerminalRefusal`, `cli.Declined`, `cli.ExitApprovalMissing`, `cli.IsStdinTTY`
- Modify (test): `wt/cmd/wt-session/approval_missing_test.go` — delete `TestApprovalMissingWording` :35 (its twin now lives in `internal/cli`); `TestLaunchQuestionDeclinedExits3` stays
- Modify: `wt/cmd/wt-model/*.go` only if 3b/3c left a copy of any of these helpers there (`grep -n 'askYesNo\|errNoTerminal\|IsTerminal\|GetSize' cmd/wt-model/*.go`): replace it with the `cli` name

**Interfaces:** Consumes Task 33. Produces nothing new; seams `openTTY`, `stdinTTY`, `stdoutWidth` keep their names and types (`func() (*os.File, error)`, `func() bool`, `func() int`).

- [ ] **Step 1: Write the failing test.** No new test: the existing suites pin every wording (`TestLaunchQuestionDeclinedExits3`, `TestAskYesNoDefaultsToNo`, the stop/rm/cloud-sync no-terminal tests). First delete `TestApprovalMissingWording` from `approval_missing_test.go` and the five helpers from `exitcode.go`.
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go build ./cmd/wt-session` → `undefined: errNoTerminal` (and the others) at each caller.
- [ ] **Step 3: Implement** the replacements listed above. No text changes.
- [ ] **Step 4: Run, expect PASS:** `cd wt && go build ./... && go vet ./... && go test ./cmd/wt-session -run 'Approval|Declined|AskYesNo|NoTerminal|Stop|ModelRm|CloudSync|Answerer|Launch|Replace|Profile' -count=1 && go test ./cmd/wt-model -count=1`.
- [ ] **Step 5: Commit** (all files above):
  `refactor(wt): use internal/cli for approval refusals, y/N questions and the terminal checks`

### Task 36: the pointer stub family (`moved.go`) and its one table test

**Files:**
- Modify: `wt/cmd/wt-session/moved.go` (Task 23 created it with `movedError` only; this task adds the rest beside it)
- Modify: `wt/cmd/wt-session/moved_test.go` (Task 23 created it with `TestMovedErrorIsOneLineExitOne`, which stays)
- Modify: `wt/cmd/wt-session/litellm.go:735-738` (move `removedCmdAnnotation` with its comment into `moved.go`; the two existing stubs keep using it)
- Modify: `wt/cmd/wt-session/main.go:641` (after `cmd.AddCommand(...)`: `addMovedStubs(cmd, movedCommands)`)
- Modify: `wt/cmd/wt-session/model.go` bare-`model` RunE (Task 24 made it return `movedError("model", "model")`): check it still does; any second pointer helper or text that crept in is deleted
- Modify: `wt/cmd/wt-session/main_test.go:1055-1062` (`TestNoWtCommandIsHidden` comment: the stubs `moved.go` registers are skipped by the same annotation)

**Interfaces:**
- Consumes: `exitcode.New`, `removedCmdAnnotation`.
- Produces (spine §10, exact):
  ```go
  type movedCommand struct{ Old, New string }   // Old "model add" (under wt), New "model add" (under wt-model)
  var movedCommands = []movedCommand{}          // empty in 4a-i; rows added by Task 37, 38, 42
  const movedAnnotation = "moved to wt-model"   // the removedCmdAnnotation value every stub carries
  func movedError(old, new string) error        // already Task 23's, unchanged: exitcode.New(1, …"<old> moved to wt-model — run 'wt-model <new>'; nothing was changed"…, false)
  func movedStub(name string, m movedCommand) *cobra.Command
  func addMovedStubs(root *cobra.Command, table []movedCommand)
  ```
  `movedStub`: `Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true, Annotations: {removedCmdAnnotation: movedAnnotation}, Args: cobra.ArbitraryArgs, RunE: return movedError(m.Old, m.New)`. `addMovedStubs`: for each row, split `Old` on spaces; the last word is the stub's name; the words before it are found as a path from `root` with `root.Find` (exact name match), and a missing one is added as `&cobra.Command{Use: word}` (not hidden); a row whose last word already exists under that parent is a programming error: `panic("moved command " + Old + " is still registered")`, so a stub can never shadow a live command.

- [ ] **Step 1: Write the failing tests** in `moved_test.go`:
  - `TestMovedCommandsPointAtWtModel` — one table over `movedCommands` plus the conditional rows `conditionalMoves = []struct{ args []string; old, new string }{{[]string{"model"}, "model", "model"}}` (Task 37 adds `model list`, 4d-ii adds `stop`). Per row, in a subtest named after `old`: `modelHome(t, writeRegistry)` plus a `config.toml` at `$XDG_CONFIG_HOME/agent-wt/config.toml` holding `default_tag = "code"\n`; read both files' bytes; `out, code := runRoot(t, args...)` where table rows use `strings.Fields(Old)` followed by `"--json", "x"` (proves flag parsing is off); assert `code == 1`, `out == "wt: " + old + " moved to wt-model — run 'wt-model " + new + "'; nothing was changed\n"` (the whole output is that one line), and both files' bytes equal the bytes read before. Before the loop, `if len(movedCommands)+len(conditionalMoves) == 0 { t.Fatal("no moved command to check; the test is vacuous") }`.
  - `TestMovedStubsAreHiddenAndAnnotated` — build `rootCmdWith(testHooks())`; for every row of `movedCommands`, `root.Find(strings.Fields(Old))` returns a command named the last word with `Hidden`, `DisableFlagParsing`, `SilenceUsage` true and `Annotations[removedCmdAnnotation] == "moved to wt-model"`.
  - `TestAddMovedStubsFindsOrAddsTheParent` — a scratch root with a child `model` (with child `list`); `addMovedStubs(root, []movedCommand{{"model add","model add"},{"served","served"},{"group sub","x"}})`; assert `model add` is a hidden stub under the existing `model` (and `model list` is untouched), `served` is a hidden stub under root, `group` was created not hidden with hidden stub `sub`; and `addMovedStubs(root, []movedCommand{{"model list","model list"}})` panics (`defer recover`, want non-nil).
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./cmd/wt-session -run 'Moved|TestNoWtCommandIsHidden' -count=1` → `undefined: movedCommands`, `movedStub`, `addMovedStubs`, `movedError`.
- [ ] **Step 3: Implement** `moved.go` per the signatures; file comment: why stubs (cobra prints the parent's help and exits 0 for an unknown name, so `wt model add … && …` would "succeed"), the wording rule (`<old>` without `wt `), and that only this binary registers them.
- [ ] **Step 4: Run, expect PASS:** `cd wt && go vet ./cmd/wt-session && go test ./cmd/wt-session -run 'Moved|TestNoWtCommandIsHidden|TestLitellmOnOffAreRetired|TestEveryRunnableCommandSilencesUsage|TestOneErrorLine' -count=1`.
- [ ] **Step 5: Commit** `cmd/wt-session/{moved.go,moved_test.go,litellm.go,main.go,model.go,main_test.go}`:
  `feat(wt): add the moved-to-wt-model pointer stubs and their one table test`

**4a-i PR gate:** the exit gate at the top. PR title `refactor(wt): internal/cli and the moved-command pointers (phase 4 stage 2, PR 4a-i)`; body `Refs` the stage-2 tracking issue (never close/fix/resolve + #N).

---

### PR 4a-ii: `wt-model model init|list|add|edit|rm`

- **Entry:** 4a-i merged. **Exit:** the gate at the top plus `cd modeltiers && make check && make test`; `git grep -nE 'wt model (init|list|add|edit|rm)' -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' | grep -v 'model list --server'` prints only lines that name the pointer itself (moved_test.go) or quote the old name as history.

### Task 37: `wt-model model list` (registry listing) moves; `wt model list` keeps `--server`

**Files:**
- Modify (before the move): `wt/cmd/wt-session/litellm.go` gains `var probeInventory = localmodels.Inventory` with its doc comment, cut from `cmd/wt-session/model_list.go:19-25` (comment reworded: "probeInventory is a test seam for the host code still in wt-session (litellm sync and the after-write route sync, until they move in 4b/4c): production probes the live local-model inventory. cmd/wt-session's TestMain stubs it to localmodels.OnDiskSnapshotForTest …; no session path reads it (the boundary test)"). wt-session still reads it after this task (`runLitellmSync`, `syncRoutesAfterWrite`, `testmain_test.go:54`, `stubProbeInventory`, `loading_test.go:38`), so the declaration must stay; Task 50 deletes it.
- Move: `git mv wt/cmd/wt-session/model_list.go wt/cmd/wt-model/model_list.go`
- Move: `git mv wt/cmd/wt-session/model_list_test.go wt/cmd/wt-model/model_list_test.go`
- Move: `git mv wt/cmd/wt-session/model_list_contract_test.go wt/cmd/wt-model/model_list_contract_test.go`
- Create: `wt/cmd/wt-session/json_shape_test.go` (the shape helpers wt-session's other contract tests still use: `jsonFieldSpec`, `jsonFieldSpecs`, `rawJSONKind`, `checkJSONShape`, copied from the moved file; users: `cleanup_contract_test.go`, `remove_contract_test.go`, `rescue_contract_test.go`, `restore_root_contract_test.go`, `host_commands_test.go`)
- Create: `wt/cmd/wt-model/fixtures_test.go` (test helpers the moved tests need and 3b did not already add to `cmd/wt-model` — check each with `grep -n 'func NAME' cmd/wt-model/*_test.go`: `modelHome`, `withCleanConfigEnv`, `mustRead`, `stubProbeInventory`, `runWTModel(t, args...) (string, error)` = execute `rootCmd(...)` as 3b builds it with out+err in one buffer)
- Modify: `wt/cmd/wt-model/testmain_test.go:16` (add `stdoutWidth = func() int { return 0 }`; `probeInventory = localmodels.OnDiskSnapshotForTest` is already there from Task 25)
- Modify: `wt/cmd/wt-session/model_list_server.go` (gains `modelListCmd(a *app) *cobra.Command`, below)
- Modify: `wt/cmd/wt-session/host_commands_test.go:88-105,205` (delete the `model list` and `model list --json` rows and the broken-registry `model list` row)
- Modify: `wt/cmd/wt-session/testmain_test.go:89` comment (stdoutWidth is `stats` and `model list --server` only)
- Modify: `wt/cmd/wt-session/moved_test.go` (`conditionalMoves` gains `{[]string{"model", "list"}, "model list", "model list"}` and `{[]string{"model", "list", "--json"}, "model list", "model list"}`)

**Interfaces:**
- Consumes: `cli.PlainColumn`, `cli.RenderPlainTable`, `cli.PlainTableWidth`, `cli.Dash`, `cli.VisibleID`; 3b's `cmd/wt-model` `host` (`hostConfig() (*config.Config, error)`) and its bare `model` command; `config.ConfigError`.
- Produces in `cmd/wt-model` (moved, signatures unchanged): no `probeInventory` (the declaration was cut into wt-session's `litellm.go` before the move; cmd/wt-model's is Task 25's `seams.go`), `var stdoutWidth = cli.StdoutWidth`, `func modelListCmd(h *host) *cobra.Command`, `type modelListJSON`, `type modelListRow` (tags unchanged), `func runModelList(out, errOut io.Writer, cfg *config.Config, asJSON bool, width int) error`, `func noteMalformed(errOut io.Writer, cfg *config.Config)`, `func fitModelList(rows []modeladmin.Row, width int) (cols []cli.PlainColumn, cells [][]string, dropped string)`, `const modelListMinKey = 20`.
- Produces in `cmd/wt-session/model_list_server.go`: `func modelListCmd(a *app) *cobra.Command` — `Use: "list"`, flags `--json` and `--server` unchanged; with `--server`: today's branch (model_list.go:64-67); without: `return movedError("model list", "model list")`. Its `Long` is today's `--server` paragraph only, led by "Without --server this is a pointer: the registry listing is `wt-model model list`."

- [ ] **Step 1: Write the failing tests.** After the three `git mv`s:
  - In `cmd/wt-model`, the moved tests keep their names: `TestModelListFitsEightyColumns`, `TestModelListDropsSizeBeforeItCutsAnId`, `TestModelListWithoutAWidthLimit`, `TestModelListJSON`, `TestModelListWithNothingToList`, `TestModelListSpellsOutControlCharacters`, `TestModelListKeepsSizeWhenEveryIdIsShort`, `TestModelListCommandGate` (builds 3b's `host` with the three fields `app` had: loaded config, `hostCfgErr`, `hostLoadErr`, per `host.go`), `TestModelListListsARowWithAMalformedFetch`, `TestModelListNamesAMalformedFetchOrDraft`, `TestModelListJSONNamesAMalformedFetchOrDraft`, `TestModelListIsQuietOnAWellFormedRegistry`, `TestModelListNamesATimePricesRowThatIsNotApplied`, `TestModelListShowsServeArgs`, `TestNoteMalformedSaysARefusedServeArgsIsNotReadAsAbsent`; `TestModelListFixtureMatchesTheDocumentTypes` and `TestModelListJSONOutputMatchesTheFixture` (the const stays `"../../../docs/contracts/model-list.sample.json"`: same depth). Assertions whose text names `wt model …` change to `wt-model model …` (the empty-registry lines, the `--json has every column` note, the `--clear-serve-args` note).
  - New `TestModelListNeedsNoModelServer` (cmd/wt-model, model_list_test.go): `modelHome(t, <registry with one openrouter model>)`, no `[model-server]`, no `WT_MODEL_*`; `runWTModel(t, "model", "list", "--json")` succeeds and the output decodes into `modelListJSON` with that model's id — the host listing never needs the session client (it is what modeltiers runs).
  - `cmd/wt-session/model_list_server_test.go`: add `TestModelListWithoutServerIsAPointer`: `modelListCmd(&app{})` with args `nil` returns an error whose `Error()` is `"model list moved to wt-model — run 'wt-model model list'; nothing was changed"` and exit code 1; with `--server` the existing three tests (`TestModelListServerMatchesTheFixture`, `…FailureEnvelope`, `…PlainColumns`) are unchanged.
  - The conditional rows above in `TestMovedCommandsPointAtWtModel`.
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./cmd/wt-model -run 'ModelList|NoteMalformed' -count=1` → `undefined: host`-related or `modelListCmd` not registered / `unknown command "list" for "wt-model model"`; `go test ./cmd/wt-session -run 'ModelListServer|ModelListWithoutServer|Moved' -count=1` → `undefined: modelListCmd`.
- [ ] **Step 3: Implement.** First (before or as part of the `git mv`): cut the `probeInventory` declaration and comment from `model_list.go` into `cmd/wt-session/litellm.go` (Files), so `go build ./cmd/wt-session` keeps building. In `cmd/wt-model/model_list.go`: package stays `main`; `modelListCmd(h *host)` drops `--server`, its branch and its `Long` paragraph (replaced by one line: "The model server's catalog as a client session sees it is `wt model list --server`, on the session machine."); `Example` becomes `"  wt-model model list\n  wt-model model list --json"`; every printed `wt model …` becomes `wt-model model …` (lines :150, :153, :167, :198); `a.hostConfig()` → `h.hostConfig()`; register it under 3b's `model` command. In `cmd/wt-session`: add `modelListCmd(a)` to `model_list_server.go`; `model.go`'s `c.AddCommand` keeps `modelListCmd(a)`.
- [ ] **Step 4: Run, expect PASS:** `cd wt && go build ./... && go vet ./... && go test ./cmd/wt-model -count=1 && go test ./cmd/wt-session -run 'ModelList|Moved|HostCommands|Contract|TestNoWtCommandIsHidden' -count=1`.
- [ ] **Step 5: Commit** (all files above, `cmd/wt-session/litellm.go` included):
  `feat(wt-model): move the registry model list to wt-model model list; wt model list keeps --server`

### Task 38: `wt-model model init|add|edit|rm`; `wt model init|add|edit|rm` become pointers

**Files:**
- Move: `git mv wt/cmd/wt-session/model_write.go wt/cmd/wt-model/model_write.go`; `git mv wt/cmd/wt-session/model_write_test.go wt/cmd/wt-model/model_write_test.go`
- Create: `wt/cmd/wt-model/model_init.go` (from `cmd/wt-session/model.go:111-178`: `modelInitJSON`, `runModelInit`, and the `init` subcommand from :73-106 as `modelInitCmd() *cobra.Command`)
- Create: `wt/cmd/wt-model/model_init_test.go` (from `cmd/wt-session/model_test.go`: `TestModelInitCreatesTheRegistryAndSaysWhatItAdded`, `TestModelInitJSON`, `TestModelInitMakesAFreshConfigUsable`, `TestModelInitSeedsAnOpenRouterRowThatFailsLoudlyWithoutItsKey`, `TestModelInitWarnsWhenConfigTomlCannotBeRead`, `TestModelInitUnderARedirectedRegistry`, `TestModelInitReportsABrokenRegistryLink`, `TestRegistryWriteGuardIsArmedHere`, and the `wt model init` part of `TestModelCommandGroup`)
- Create: `wt/cmd/wt-model/help_test.go` with `TestHelpNamesSushiWhereItListsProviders` (the `{model init}`, `{model add}` half of `cmd/wt-session/model_cmds_test.go:840`; the `{stop}` half stays there until 4d-ii)
- Modify: `wt/cmd/wt-session/model.go` (delete `seedEnv` :20 only if no wt-session file still uses it — `commands_config.go` no longer does after 3c, `cloudsync.go` does until 4b: check with grep; delete `modelInitJSON`, `runModelInit`, the `init` subcommand; `modelCmd` keeps the bare pointer and `modelListCmd(a)`)
- Modify: `wt/cmd/wt-session/model_test.go` (delete the moved tests; `stubSeedEnv`, `stubRouteSync`, `realRouteSync`, `onPath` stay — `cloudsync_*_test.go`, `host_commands_test.go`, `load_error_hint_test.go` use them; if the file then holds only helpers, rename it `git mv … host_fixtures_test.go`)
- Create: `wt/cmd/wt-session/host_fixtures_test.go` holding the helpers `model_write_test.go` defined that wt-session tests still use (`modelHome`, `mustRead`, `runWT`, `runWTWith`, `redirectedRegistry`, `writeRegistry`; find users with `grep -ln 'modelHome(\|mustRead(\|runWT(\|runWTWith(\|redirectedRegistry(\|writeRegistry' cmd/wt-session/*_test.go`)
- Modify: `wt/cmd/wt-session/testmain_test.go:94-99` (delete `ollamaCaps`, `confirmRemove` stubs; keep `seedEnv`/`syncRoutesAfterWrite` while cloudsync is here)
- Modify: `wt/cmd/wt-model/testmain_test.go` (add `confirmRemove` → error "confirmRemove not stubbed in this test" and `litellm.StubProxyReadinessForTest` exactly as `cmd/wt-session/testmain_test.go:123-128`; `ollamaCaps`, `seedEnv`, `syncRoutesAfterWrite` are Task 25's, already identical to wt-session's)
- Modify: `wt/cmd/wt-model/fixtures_test.go` (add `stubSeedEnv`, `stubRouteSync`, `realRouteSync` (copied verbatim: it assigns `syncRoutesAfterWrite = realSyncRoutesAfterWrite`, both declared in Task 25's `seams.go`), `onPath`, `stubOllamaCaps`, `stubConfirmRemove`, `sp`, `redirectedRegistry`, `writeAgentsToml`, `writeRegistry` where missing)
- Modify: `wt/cmd/wt-session/load_error_hint_test.go` (`TestModelInitNamesTheRepairForARegistryItCannotWrite` moves to `cmd/wt-model/load_error_hint_test.go`; the other three stay, with `wt model init` in their `never`/count assertions read as `wt-model model init`, matching Task 39)
- Modify: `wt/cmd/wt-session/host_commands_test.go:106-111` (delete the `model add` row)
- Modify: `wt/cmd/wt-session/moved.go` (`movedCommands` gains `{"model init","model init"}`, `{"model add","model add"}`, `{"model edit","model edit"}`, `{"model rm","model rm"}`)
- Modify: `wt/cmd/wt-model/main.go:34` (register `modelInitCmd()`, `modelAddCmd(h)`, `modelEditCmd(h)`, `modelRmCmd(h)` under 3b's `model` command). Also `:30`: drop the root's `SilenceUsage: true` and end `rootCmd` with `silenceUsageOnError(root)`, a copy of `cmd/wt-session/main.go:650-664` (the walk and its comment; no annotation constant is needed yet: it silences every runnable command, and `model edit` turns usage back on for its two usage mistakes itself, `model_write.go:284,296`). Cobra prints usage only when neither the root nor the failing command silences it, so with the root silenced the moved `model edit` would lose its usage block; this keeps today's output shape.
- Modify: `wt/cmd/wt-session/error_line_test.go:53,57` (`TestOneErrorLineThroughTheRealTree`): the row `"error from a leaf RunE": {model rm nosuch/nosuch --yes}` stays as is (the pointer stub's RunE is a leaf RunE: one `wt: ` line, no usage, exit 1); the row `"model edit usage mistake"` becomes `"pointer swallows a usage mistake": {[]string{"model", "edit", "nosuch/nosuch"}, 1, false}` (the stub prints the one pointer line and no usage).
- Create: `wt/cmd/wt-model/error_line_test.go` with `TestOneErrorLineThroughTheWtModelTree` (Step 1)

**Interfaces:**
- Consumes: `cli.NoTerminalRefusal`, `cli.Declined`, `cli.AskOnTTY`, `cli.ErrNoTerminal`; 3a's `litellm.SyncAndWarn(out, errOut io.Writer, warnings []string, sync func(out, errOut io.Writer) string) error` and `cmd/wt-model`'s `syncRoutesAfterWrite`, `seedEnv`, `ollamaCaps`, `probeInventory` (3b, spine §9); 3b's `host`.
- Produces in `cmd/wt-model` (moved; signatures as today with `*app` → `*host`): `modelFieldFlags(c *cobra.Command) func() modeladmin.Fields`, `modelAddCmd(h *host)`, `absDotRelative(side string) (string, error)`, `repoThatIsADirectory(sides ...string) []string`, `runModelAdd(out, errOut io.Writer, cfg *config.Config, req modeladmin.AddRequest) error`, `enableSushiForAgents(out io.Writer, providerID string) []string`, `modelEditCmd(_ *host)`, `runModelEdit(out, errOut io.Writer, id string, f modeladmin.Fields) error`, `unknownModelHint(err error) error`, `modelRmCmd(h *host)`, `runModelRm(out, errOut io.Writer, cfg *config.Config, ids []string, yes bool) error`, `uniqueInOrder(ids []string) []string`, `modelInitCmd() *cobra.Command`, `runModelInit(out, errOut io.Writer, asJSON bool) error`, `type modelInitJSON` (tags unchanged); `var openTTY = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }` (unless 3c declared one); `var confirmRemove = func(q string) (bool, error) { return cli.AskOnTTY(openTTY, q, cli.ErrNoTerminal) }`; `func withRegistryHint(err error) error` (copy of `cmd/wt-session/cloudsync.go:679`; Task 45 Step 1 deletes this copy when cloudsync.go moves in, so the package keeps one).

- [ ] **Step 1: Write the failing tests.** After the moves, the moved tests keep their names; in `cmd/wt-model/model_write_test.go` they are `TestModelAddOnAFreshMachine`, `TestModelAddAsksOllamaWhatTheModelCanDo`, `TestModelCommandsThroughTheCommandLine` (drives `runWTModel(t, "model", …)`), `TestModelRmWithoutATerminalNeedsYes` (stubs `openTTY` to fail, expects exit 3 and `"approval missing: pass --yes (there is no terminal to ask); nothing was removed"`), `TestModelRmStillWorksWhenTheConfigDoesNotLoad`, `TestModelWritesUnderARedirectedRegistry`, `TestModelRmRefusesAnUnknownIdBeforeItAsks`, `TestModelRmCommandRunsWhenTheConfigDidNotLoad`, `TestModelEditRefusesAnIDTheRegistryHoldsTwice`, `TestModelRmRefusesAnIDTheRegistryHoldsTwice`, `TestModelRmRefusesADuplicatedIDWhenTheConfigDidNotLoad`, `TestModelAddOfADuplicatedIDStillSaysItExists`, `TestModelAddAPairing`, `TestModelEditAndRmOfAPairing`, `TestModelEditSourceFlag`, `TestModelEditServeArgFlags`, `TestSushiAddListsSushiForClaudeAndPi`. Text assertions naming `wt model …` become `wt-model model …` (`run \`wt-model model rm %s\``, `(\`wt-model model list\` shows every id)`, `run \`wt-model model init\` again`). New in `model_init_test.go`: `TestModelCommandGroupOnWtModel` — `runWTModel(t,"model","init","--json")` contains `"providers_added":["ollama"]` (with `stubSeedEnv(onPath("ollama"))`, `stubRouteSync(t,"")`); `model bogus` → error containing `unknown command "bogus" for "wt-model model"`; `model init extra` → error. In `cmd/wt-session`: the four rows join `TestMovedCommandsPointAtWtModel` through `movedCommands`, and `TestMovedStubsAreHiddenAndAnnotated` covers them; plus the `error_line_test.go` row change in Files. New in `cmd/wt-model/error_line_test.go`:
  ```go
  // TestOneErrorLineThroughTheWtModelTree pins the error shape of the moved
  // registry commands on wt-model: one "wt-model: " line, never cobra's
  // "Error:" line, and the usage block before it exactly where wt showed one
  // (a model edit usage mistake), so the move changes no output shape.
  func TestOneErrorLineThroughTheWtModelTree(t *testing.T) {
  	// rows: {"model","edit","nosuch/nosuch"} → code 1, usage true (the twin of wt-session's old row);
  	//       {"model","rm","nosuch/nosuch","--yes"} → code 1, usage false.
  	// each: root := rootCmd(fakeDeps); out and err into one buffer; err := root.Execute();
  	//   code := exitcode.Report("wt-model", err, &buf); no "Error:"; exactly one line with prefix "wt-model: ";
  	//   strings.Contains(out, "Usage:") == usage; code == 1.
  }
  ```
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./cmd/wt-model -run 'ModelInit|ModelAdd|ModelEdit|ModelRm|ModelCommand|Sushi|RegistryWriteGuard|NamesTheRepair|HelpNamesSushi|OneErrorLine' -count=1` → undefined `modelAddCmd`/`host` mismatches or `unknown command "init" for "wt-model model"`; `go test ./cmd/wt-session -run Moved -count=1` → `moved command model init is still registered` panic.
- [ ] **Step 3: Implement** the moves and edits listed. In the moved help texts every `wt model …` example and sentence becomes `wt-model model …`, `wt cloud-sync` stays (4b moves it); the route-sync behaviour is unchanged (`syncAndWarn` → `litellm.SyncAndWarn(out, errOut, warnings, syncRoutesAfterWrite)` as 3a left it). Output lines, JSON tags, flags and exit codes are byte-for-byte today's except the command names inside hint text.
- [ ] **Step 4: Run, expect PASS:** `cd wt && go build ./... && go vet ./... && go test ./cmd/wt-model -count=1 && go test ./cmd/wt-session -run 'Moved|HostCommands|LoadError|CloudSync|Litellm|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestNoHelpTextNamesModelman|TestHelpNamesSushi|TestOneErrorLineThroughTheRealTree' -count=1 && go test ./internal/depcheck -count=1` (`cmd/wt-model` still links nothing in `sessionBanned`).
- [ ] **Step 5: Commit** (all files above):
  `feat(wt-model): move model init, add, edit and rm to wt-model; the wt names print a pointer`

### Task 39: user-visible Go strings name `wt-model model …`

**Files** (find each with `cd wt && git grep -nE 'wt model (init|list|add|edit|rm)' -- '*.go' | grep -v 'model list --server'`; change **printed strings and help text**; change a comment where it names the command a user runs; leave `modelclient/drive.go`'s `wt model list --server` and `docs/contracts/job.sample.json`):
- Modify: `wt/internal/config/registry.go:189`, `wt/internal/config/registry_write.go:117` (`seed it with \`wt-model model init\``), comments `config.go:1192`, `registry.go:184`, `registry_seed.go:131`
- Modify: `wt/internal/lifecycle/lifecycle.go:224` (`\`wt-model model edit %s --clear-serve-args\``)
- Modify: `wt/internal/modeladmin/apply.go:178,181` (`edit that one (wt-model model edit %s)`)
- Modify: `wt/cmd/wt-session/smoke.go:554` (`add one on the server host with wt-model model add`)
- Modify: `wt/cmd/wt-session/cloudsync.go:201` (`\`wt-model model add\``), `wt/cmd/wt-session/litellm.go:224` (`\`wt-model model init\` adds the default ones`)
- Modify: `wt/internal/modeleditor/models_form.go` (was configeditor :506-507; the pairing hints: `wt-model model add mlx_lm_server …`)
- Modify (tests pinning those strings): `wt/cmd/wt-session/helpers_test.go:457`, `cloudsync_test.go:307`, `litellm_test.go:1643,2091,2129`, `load_error_hint_test.go:49,68,77,88,98,144`, `flip_test.go:462`, and whatever `cd wt && git grep -n 'seed it with\|wt model init\|wt model edit\|wt model add' -- '*_test.go'` lists in `internal/config`, `internal/lifecycle`, `internal/modeladmin`, `internal/modeleditor`

**Interfaces:** none.

- [ ] **Step 1: Write the failing test.** Update the pinned assertions first (e.g. `helpers_test.go:457` → `" — seed it with \`wt-model model init\`"`; `load_error_hint_test.go:144` counts `"wt-model model init"` == 1). Add to `wt/internal/config/registry_test.go` `TestMissingRegistryNamesWtModelInit`: with a scratch `XDG_CONFIG_HOME` and no registry, `config.Load()`'s error contains `"seed it with \`wt-model model init\`"` and does not contain `"\`wt model init\`"` — the session binary prints this line too, so it must name the binary that has the command.
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./internal/config -run TestMissingRegistryNamesWtModelInit -count=1` → message still `wt model init`.
- [ ] **Step 3: Implement** the string edits listed.
- [ ] **Step 4: Run, expect PASS:** `cd wt && go test ./internal/config ./internal/lifecycle ./internal/modeladmin ./internal/modeleditor -count=1 && go test ./cmd/wt-session -run 'LoadError|Helpers|CloudSync|Litellm|Flip|Smoke' -count=1 && go test ./cmd/wt-model -count=1`.
- [ ] **Step 5: Commit** (the files above):
  `fix(wt): name wt-model model init|add|edit in every printed repair hint`

### Task 40: modeltiers reads `wt-model model list --json`

**Files:**
- Modify: `modeltiers/src/modeltiers/inventory.py:1,13,16,21,33` (`WT_COMMAND = ("wt-model", "model", "list", "--json")`; docstrings; the not-found text `"wt-model is not on PATH; install it (make install) or pass --inventory FILE"`)
- Modify: `modeltiers/src/modeltiers/cli.py:115` (`help="read saved \`wt-model model list --json\` output instead of running wt-model"`), `errors.py:13`, `records.py:19` (docstrings)
- Modify: `modeltiers/tests/test_inventory.py:122-145` (expected argv and messages), `modeltiers/tests/test_conftest_guards.py:21` (`subprocess.run(["wt-model", "model", "list", "--json"])`), `modeltiers/tests/conftest.py:37-41` (comment: the Go test now lives at `wt/cmd/wt-model/model_list_contract_test.go`; `INVENTORY` unchanged)
- Modify: `modeltiers/CLAUDE.md`, `modeltiers/README.md`, `modeltiers/docs/internals.md`, `modeltiers/tests/fixtures/README.md`, `docs/guides/12-model-tiers.md` (every `wt model list --json` → `wt-model model list --json`; the contract-test path → `cmd/wt-model/model_list_contract_test.go`)

**Interfaces:** `inventory.run_wt() -> str` keeps its name (a seam named in `cli.Deps`; renaming is out of scope).

- [ ] **Step 1: Write the failing test.** In `tests/test_inventory.py` change the run test's assertion to `assert fake.calls == [["wt-model", "model", "list", "--json"]]` and the not-on-PATH test to `match="wt-model is not on PATH"`.
- [ ] **Step 2: Run, expect FAIL:** `cd modeltiers && uv run pytest tests/test_inventory.py -q` → the argv still starts `"wt"`.
- [ ] **Step 3: Implement** the edits listed. `test_guide_samples.py` pins guide 12's ```` ```text ```` blocks to `run`'s output: if a sample quotes the command line, regenerate the sample per `modeltiers/CLAUDE.md` rather than hand-editing.
- [ ] **Step 4: Run, expect PASS:** `cd modeltiers && make check && make test`.
- [ ] **Step 5: Commit** (the files above):
  `feat(modeltiers): read the inventory from wt-model model list --json`

### Task 41: scripts, the quantize hint, skills and docs call `wt-model model …`

**Files:**
- Modify: `benchmarks/mtplx-profile-matrix:74` (`command -v wt > /dev/null && command -v wt-model > /dev/null && command -v claude > /dev/null || { echo "wt, wt-model and claude must be on PATH"; exit 1; }`: preflight now calls `wt-model model list` with stderr discarded, so without this a missing wt-model reads as "$MODEL is not in the registry"), `:5,68,76,81,153,154` (`wt-model model edit "$MODEL" …`, `wt-model model list 2> /dev/null | awk 'NR>1 && ($5=="run" || $5=="load") {print $1}'` — column 5 is RUNNING, unchanged by Task 37 because `fitModelList` never drops the first five columns; `:69,222` `wt stop mtplx` stay until 4d-ii) and `benchmarks/mtplx-profile-matrix.md`
- Modify: `bin/mlx-quantize:13,79` (`echo "wt-model model add omlx <directory name> --family <family>."`)
- Modify skills: `.claude/skills/wt-mlx-lm-quantization/SKILL.md` (:3 description, :9, :12, :17), `.claude/skills/wt-repo-cleanup/SKILL.md:73`, `.claude/skills/wt-adding-a-benchmark-backend/SKILL.md:102,108`, `wt/.claude/skills/wt-adding-a-provider/SKILL.md:18`, `wt/.claude/skills/wt-cloud-sync/SKILL.md:163,171,216` (only its `wt model …` mentions; `wt cloud-sync` stays until 4b). (`wt-manage-slot-profiles` has no `wt model …` command; its :141 names `wt cloud-sync`, which is Task 47's.)
- Modify docs: the list from `git grep -lE 'wt model (init|list|add|edit|rm)' -- '*.md' ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md'` (today about 40 files: `CLAUDE.md`, `wt/CLAUDE.md`, `wt/README.md`, `wt/docs/wt-model.md`, `wt/docs/internals/*.md`, `wt/docs/wt-*.md`, `docs/guides/00,01,02,03,04,05,06,08,10,11,12,13`, `docs/reference/*.md`, `benchmarks/*.md`, `llmbench/CLAUDE.md`). **Rule:** a command a reader runs (`wt model init|list|add|edit|rm`) becomes `wt-model model …`, except in session pages (`wt/docs/wt-job.md`, `wt-session.md`, `wt-smoke.md`, `wt-start-stop.md`, `wt/docs/wt-agents/*`), which a reader opens on a session machine: there a check of a model's state becomes `wt model list --server` (e.g. `wt-job.md:49` "Check the model itself with `wt model list --server`"), and `wt-agents/shell-wt.md:51`'s example `shell-wt --cwd wt model list` becomes a session command (`shell-wt --cwd wt worktrees --plain`); `wt model list --server` stays; dated records (`docs/archive`, `docs/superpowers`, `benchmarks/results`, CHANGELOG history) stay; a sentence that explains *where* the registry commands live gains "(on the machine that runs wt-model)" only where it already says "host". Guides keep no live model state. `wt/docs/wt-model.md` keeps its file name (bin/check-links); its title and first paragraph say the commands are `wt-model model …` and that `wt model …` prints a pointer.
- Modify: `wt/CHANGELOG.md` (new top entry: "`wt model init|list|add|edit|rm` moved to `wt-model model …`; the old names print a one-line pointer and exit 1; `wt model list --server` stays")
- Modify: `wt/CLAUDE.md:316` (the "wt-model" bullet: "**`wt-model` is the binary; `wt-model model` is the registry command, and `wt model` in wt is only `wt model list --server` plus pointers** (#600): write them that way in prose") and `wt/docs/style-guide.md` D2 (:161: "`docs/wt-model.md` is the registry command `wt-model model` (the file name is kept); in prose the binary is written `wt-model`, the session's catalog view `wt model list --server`").
- Modify: `bin/update/wt` and `docs/guides/08-maintenance-and-troubleshooting.md:346`: the whole `bin/update/wt` change described in Task 70's Files (a missing `wt-model` reads as stale; `INSTALLED_MODEL`, `MISSING_NAMES`, the three-name trailers), moved here because from this PR modeltiers needs `wt-model` on PATH (and llmbench from 4a-iii), so an install without it must not read "current". Task 70 only checks it.
- Modify Go/JSON fixture comments the grep lists that are not printed (`docs/contracts/registry.sample.toml` comment, `benchmarks/suites/eval-sushi-vs-ollama.toml` comment)

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Run `git grep -nE 'wt model (init|list|add|edit|rm)' -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' | grep -v 'model list --server' | grep -v 'moved_test.go'` → today it lists every file above (the "failure").
- [ ] **Step 2: Run** `bash -n benchmarks/mtplx-profile-matrix && grep -c 'wt-model model edit' benchmarks/mtplx-profile-matrix` → `0` today. (A `--dry-run` cannot show it: `preflight` runs even with `--dry-run`, :213, and exits on a missing `claude`, an unregistered model or a non-ok slot profile before any `run` line.) Also run Task 70's Step 1 check for `bin/update/wt` (scratch HOME with only `wt` and `wt-session`) → today `[update:wt] current — wt 9.9.9`.
- [ ] **Step 3: Implement** the edits by the rule above.
- [ ] **Step 4: Run, expect PASS:** the Step 1 grep prints nothing (pointer quotes follow the "Quoting an old name" constraint, so none matches); `make lint-shell && make lint-skills && bin/check-links`; `cd modeltiers && uv run pytest tests/test_guide_samples.py -q`; `bash -n benchmarks/mtplx-profile-matrix && grep -c 'wt-model model edit' benchmarks/mtplx-profile-matrix` → `3` (:68, :153, :154) and `grep -n 'command -v wt-model' benchmarks/mtplx-profile-matrix` → the :74 line; Task 70's Step 4 `bin/update/wt` checks (stale naming `wt-model`; current with a fake `wt-model`; not-installed with none, exit 3).
- [ ] **Step 5: Commit** (the files above):
  `docs: call wt-model model init|list|add|edit|rm in scripts, skills and guides`

**4a-ii PR gate:** the exit gate at the top plus `cd modeltiers && make check && make test`. PR title `feat(wt-model): model init|list|add|edit|rm move to wt-model (phase 4 stage 2, PR 4a-ii)`; body says the old names print a pointer and exit 1, lists the callers moved, and uses `Refs #N` only. The body's "After merge" section tells the user to reinstall the globally installed `wt-repo-cleanup` skill (`make install-skill-claude`, and `make install-skill-pi` / `make install-skill-codex` where used), since this PR changed its `wt model init` line and installed copies do not update themselves.

---

### PR 4a-iii: `wt-model served` and `wt-model warm`

- **Entry:** 4a-ii merged. **Exit:** the gate at the top plus `cd llmbench && make check && make test`; `git grep -nE '(^|[^-[:alnum:]_/])wt (served|warm)([^[:alnum:]_-]|$)' -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` prints nothing (the pattern is the portable form Task 75 uses: `\b` matches nothing in this git's ERE, so a `\b` grep passes vacuously).

### Task 42: `served` and `warm` move to `cmd/wt-model/served.go`

**Files:**
- Create: `wt/cmd/wt-model/served.go` (from `cmd/wt-session/model_cmds.go:710-811`: `servedProbeTimeout`, `servedFamilies`, `servedCmd`, `runServed`, `warmCmd`, `runWarm`)
- Create: `wt/cmd/wt-model/served_test.go` (from `cmd/wt-session/model_cmds_test.go`: `TestServedReportsWhatAKeyedOmlxHasLoaded`, `TestRunServedSushiListsOnlyLoadedPacks`, `TestServedFailsRatherThanAnswerNothing`, `TestServedRefusesOllama`, `TestWarmSendsTheRegistryKeyToAKeyedOmlx`, `TestWarmRefusesOtherProviders`, with the helpers only they use)
- Create: `wt/cmd/wt-model/host_test.go` `TestHostCommandsStillLoadTheRegistry` (the table later PRs extend: Task 45 adds cloud-sync, Task 59 stop; it starts with the `served`, `warm` rows and their broken-registry rows of `cmd/wt-session/host_commands_test.go:128-135,207-208`, rebuilt over 3b's `host`)
- Modify: `wt/cmd/wt-session/model_cmds.go` (delete :710-811 and the imports only they used; file comment :1-5 drops the `wt served` sentence)
- Modify: `wt/cmd/wt-session/model_cmds_test.go` (delete the six moved tests), `host_commands_test.go` (delete the `served`/`warm` rows; the remaining ones are `cloud-sync`, `litellm list`, `stop provider`)
- Modify: `wt/cmd/wt-session/main.go:641` (drop `servedCmd(a), warmCmd(a)`), `wt/cmd/wt-session/main_test.go:1045` (`TestRootHelpListsModelSubcommands` drops `"served"`)
- Modify: `wt/cmd/wt-session/moved.go` (rows `{"served","served"}`, `{"warm","warm"}`)
- Modify: `wt/cmd/wt-model/main.go:34` (`root.AddCommand(…, servedCmd(h), warmCmd(h))`)

**Interfaces:**
- Consumes: 3b's `host`; `localmodels.ServedIDs`, `lifecycle.Warm`, `provider.ServedFamilies` (admin packages `cmd/wt-model` already links).
- Produces in `cmd/wt-model` (signatures unchanged, `*app` → `*host`): `servedCmd(h *host) *cobra.Command`, `runServed(out io.Writer, cfg *config.Config, client *http.Client, family string, asJSON bool) error`, `warmCmd(h *host) *cobra.Command`, `runWarm(ctx context.Context, out io.Writer, cfg *config.Config, provider, model string) error`. `warm` uses `cmd.Context()` (wt-model's `main` already cancels it on SIGINT/SIGTERM, main.go:42) instead of wt-session's `startSignalCtx`.

- [ ] **Step 1: Write the failing tests.** The six moved tests keep their names; text assertions change only in command names: `unknown provider "ollama": wt-model served answers for …`, `unknown provider %q: wt-model warm loads models into omlx only`. `TestHostCommandsStillLoadTheRegistry` (cmd/wt-model, new): for `served nosuch` and `warm ollama m` the error equals `runServed`/`runWarm` over `config.Load()`'s config (as host_commands_test.go:128-135), the host config was read, and over a registry `models = [\n` both return `config.ConfigError(loadErr)`. The two rows join `TestMovedCommandsPointAtWtModel` (e.g. `wt served omlx --json x` → `wt: served moved to wt-model — run 'wt-model served'; nothing was changed`).
- [ ] **Step 2: Run, expect FAIL:** `cd wt && go test ./cmd/wt-model -run 'Served|Warm' -count=1` → `undefined: servedCmd`; `go test ./cmd/wt-session -run Moved -count=1` → panic `moved command served is still registered`.
- [ ] **Step 3: Implement** the move; help texts: `Example: "  wt-model served omlx\n  wt-model served mtplx --json"`, `"  wt-model warm omlx Qwen3.8-27B-4bit"`; `Long` sentences "the probe `wt start`, `wt stop` and the pickers act on" and "this is the warmup step of `wt start`" stay (they name session commands).
- [ ] **Step 4: Run, expect PASS:** `cd wt && go build ./... && go vet ./... && go test ./cmd/wt-model -count=1 && go test ./cmd/wt-session -run 'Moved|HostCommands|Stop|TestRootHelp|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestNoHelpTextNamesModelman' -count=1 && go test ./internal/depcheck -count=1`.
- [ ] **Step 5: Commit** (all files above):
  `feat(wt-model): move served and warm to wt-model; the wt names print a pointer`

### Task 43: llmbench's bridge calls `wt-model warm` and `wt-model served`

**Files:**
- Modify: `llmbench/src/llmbench/wt_bridge.py` (:1-5 docstring; :25-28 `ensure_wt` → `ensure_binary(name: str) -> None` raising `WtNotFoundError(f"{name} not found on PATH; install it with \`make install\`")`; :42 `_msg` strips `"Error: "`, `"wt-model: "` and `"wt: "`; :55-78 `warm` runs `["wt-model", "warm", provider, model]`, timeout text `"wt-model warm timed out after {timeout:g}s"`, OSError text `"wt-model warm could not run: …"`; :87-98 `_run(argv, timeout)` calls `ensure_binary(argv[0])` and its FileNotFoundError text names `argv[0]`; :101-108 `served` runs `["wt-model", "served", provider]`; `stop` keeps `["wt", "stop", model_ref, "--yes"]`; `_NOT_CONFIGURED_HINT` :124-127 reads `"… choose local or remote in wt config (Mode tab), or set WT_MODEL_URL and WT_MODEL_TOKEN_FILE"` if 3c did not already change it)
- Modify: `llmbench/src/llmbench/providers/lifecycle/backends/omlx.py:118,158` (docstrings: `wt-model warm`, `wt-model served omlx`, `wt stop`)
- Modify: `llmbench/tests/test_wt_bridge.py` (:1 docstring; :16 fixture `which` returns `f"/usr/local/bin/{name}"`; :33 `["wt-model", "warm", "omlx", "Qwen-4bit"]`; :53-56 messages; :147 `["wt-model", "served", "omlx"]`; :169 unchanged `["wt", "stop", …]`)
- Modify: `llmbench/tests/conftest.py:64` (`_FAKE_BINARIES` gains `"wt-model"`), `llmbench/tests/providers/lifecycle/test_hermeticity.py` (the pinned allow-list gains `wt-model`)
- Modify: `llmbench/CLAUDE.md` ("Monorepo context" bullet: `wt-model warm`, `wt-model served omlx`, `wt stop omlx/<name>`), `llmbench/docs/internals.md` if it names them

**Interfaces:** `warm(provider: str, model: str, timeout: float = WARM_TIMEOUT) -> None`, `served(provider: str, timeout: float = SERVED_TIMEOUT) -> list[str]`, `stop(model_ref: str, timeout: float = STOP_TIMEOUT) -> None` unchanged; `ensure_wt()` is replaced by `ensure_binary(name: str) -> None` (no other caller: `grep -rn ensure_wt llmbench/` lists only wt_bridge.py).

- [ ] **Step 1: Write the failing tests** in `tests/test_wt_bridge.py`: update the argv assertions above; add `test_warm_and_served_need_wt_model_on_path`: `which` returns a path for `"wt"` only; `warm("omlx","m")` and `served("omlx")` each raise `WtNotFoundError` matching `"wt-model not found on PATH"`, while `stop("omlx/m")` runs (fake run returns 0) — the two binaries are checked separately; add `test_msg_strips_the_wt_model_prefix`: `_msg(CompletedProcess([], 1, "", "wt-model: x\n"), "f") == "x"`. In `tests/test_conftest_guards.py` (or `test_hermeticity.py`, wherever the basename list is pinned) assert `"wt-model"` is faked.
- [ ] **Step 2: Run, expect FAIL:** `cd llmbench && uv run pytest tests/test_wt_bridge.py tests/providers/lifecycle/test_hermeticity.py -q` → argv starts `"wt"`, `wt-model` not in the guard.
- [ ] **Step 3: Implement** the edits listed.
- [ ] **Step 4: Run, expect PASS:** `cd llmbench && make check && make test`.
- [ ] **Step 5: Commit** (the files above):
  `feat(llmbench): warm and list omlx models through wt-model warm and served`

### Task 44: docs and skills name `wt-model served|warm`

**Files:** the list from `git grep -lE '(^|[^-[:alnum:]_/])wt (served|warm)([^[:alnum:]_-]|$)' -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` (do not use `\b`: it matches nothing in this git's ERE). On the entry tree, after Tasks 42–43 (which own `wt/cmd/wt-session/model_cmds*.go`, `llmbench/src/llmbench/wt_bridge.py`, `backends/omlx.py` and `llmbench/tests/test_wt_bridge.py`), expect: `docs/guides/00-config-map.md`, `02-providers-and-models.md`, `05-benchmarks.md`, `08-maintenance-and-troubleshooting.md`, `llmbench/CLAUDE.md`, `llmbench/tests/conftest.py`, `llmbench/tests/providers/lifecycle/backends/test_omlx.py`, `llmbench/tests/providers/lifecycle/test_probe.py`, `wt/CLAUDE.md`, `wt/README.md`, `wt/docs/internals/config-and-registry.md`, `local-models.md`, `packages.md`, `testing.md`, `wt/docs/wt-session.md`, `wt/docs/wt-start-stop.md`, and the Go comments `wt/internal/lifecycle/omlx.go:46`, `wt/internal/lifecycle/backends_test.go`, `wt/internal/localmodels/inventory_test.go`, `served_test.go`, `wt/internal/provider/provider.go:106` (Python test edits are comments/docstrings only; run `cd llmbench && uv run pytest -q` after them), `.claude/skills/wt-adding-a-benchmark-backend/SKILL.md` (any `wt served|warm` step), `wt/CHANGELOG.md` (new top entry for served/warm). Same rule as Task 41.

**Interfaces:** none.

- [ ] **Step 1: Failing check:** the grep above lists the files.
- [ ] **Step 2:** none to run beyond Step 1.
- [ ] **Step 3: Implement** by the Task 41 rule.
- [ ] **Step 4: Run, expect PASS:** the grep prints nothing (the pointer table and its test are excluded); `make lint-skills && bin/check-links`; `cd wt && go vet ./internal/lifecycle ./internal/provider ./internal/localmodels`; `cd llmbench && uv run pytest -q`.
- [ ] **Step 5: Commit:** `docs: call wt-model served and warm in guides, skills and package notes`

**4a-iii PR gate:** the exit gate at the top plus `cd llmbench && make check && make test`. PR title `feat(wt-model): served and warm move to wt-model (phase 4 stage 2, PR 4a-iii)`; body `Refs #N` only.

---

## PR 4b: cloud-sync moves to wt-model

**Entry:** PR 4a-iii merged: `cmd/wt-model` has `host.go` (`type host struct`, copied in 3b from `cmd/wt-session/app.go:204-252`: fields `hostCfg`, `hostLoadErr`, `hostCfgErr`, `hostSet`, method `hostConfig() (*config.Config, error)`), `model init|list|add|edit|rm`, `served`, `warm`, the seams `probeInventory`, `syncRoutesAfterWrite`, `openTTY` and the test helpers `modelHome`, `mustRead`, `withCleanConfigEnv`, `stubRouteSync`, `realRouteSync` (moved with `model_write_test.go`/`model_test.go` in 4a-ii); `internal/cli` exists; `cmd/wt-session/moved.go` holds `movedCommands` (model init|add|edit|rm, served, warm) and `moved_test.go` holds `TestMovedCommandsPointAtWtModel`.
**Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (run in the background, ~10 min) and, named, `go test ./internal/depcheck ./internal/removal ./cmd/wt-model -count=1`; from the repo root `make lint` (lint-shell, lint-skills, check-links); `git grep -n "wt cloud-sync" -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` prints nothing; `git grep -n "wt: cloud-sync:" -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md'` prints nothing (pointer quotes follow the "Quoting an old name" constraint, so they never match).
**Exit state:** `wt-model cloud-sync` is today's command, byte for byte in flags (`--only --dry-run --yes --html --approve-removals --force`), plans, prompts and exit codes 0–5 (error lines now start `wt-model: `, the binary's own prefix); `wt cloud-sync …` prints `wt: cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed` and exits 1; the stale-pricing notice says `run 'wt-model cloud-sync'`.

Today's tests and what happens to each (all on `origin/main` f60ad48a, `wt/cmd/wt-session/`):

| File | Tests | Fate |
|---|---|---|
| `cloudsync_test.go` (1153 lines) | `TestCloudSyncSeamsFailClosed`, `TestRealCloudFetch`, `TestSelectFlows`, `TestCloudSyncCommandRefusals`, `TestCloudSyncOpenRouterDryRun`, `…OpenRouterApply`, `…StampOnlyRunSkipsTheRouteSync`, `…OpenRouterFailuresExit1`, `…WithNoOpenRouterModelFetchesNothing`, `…AsksOnceAndTakesNoForAnAnswer`, `TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo`, `TestCloudSyncPrefixesTheRouteSyncsLines`, `…OpenRouterRefusesWhenTheRegistryChangedAfterThePlan`, `…ReportsARefusedRegistryWrite`, `…UnderARedirectedRegistry`, `…WithNoOpenRouterPricedModelDoesNothingHoweverItIsRun`, `…RefreshesOnlyTheOpenRouterProvidersModels`, `…SaysWhenNoModelCouldBeRefreshed`, `…RefusesADuplicatedModelIDBeforeThePlan`, `…RefusedPlanDoesNotCreateARegistry`, `…TimeOfDayPricesDoNotFollowTheClock`, `…RowsOnlyUpdateLeavesTheRoutesAlone`, `…UnreadableScheduleIsAWarningNotAFailure`, `TestGraduatedClause` | git mv → `cmd/wt-model/cloudsync_test.go`; `&app{…}` → `&host{…}` (lines 280-307); gains `TestWtModelServesCloudSync`, `TestCloudSyncExitLinesNameWtModel` |
| `cloudsync_catalog_test.go` (1129) | `TestCloudSyncOllama*` (20), `TestCloudSyncFlowsAreIndependent`, `…SkipsOllama*`, `…WithNeitherProviderDoesNothing`, `…RefusedPlansDoNotCreateARegistry`, `…BothFlowsInOneRun`, `…FinishesAnInterruptedRun`, `…WritesEachFlowAloneWhenARowWouldNotLoad`, `TestSaveFailedHTMLNeverWritesThroughALink` | git mv → `cmd/wt-model/`; line 583 `&app{…}` → `&host{…}`; line 498's expected text changes in Task 46 |
| `cloudsync_flownames_test.go` (167) | `TestCloudSyncFlowsAreNamedForTheirProviders`, `TestCloudFlowsMatchTheirProviders`, `TestPendingFlowsFollowTheTableOrder`, `TestPendingFlowsPanicsWhenAFlowIsMissing` | git mv; lines 28, 105 `&app{…}` → `&host{…}` |
| `cloudsync_ollama_test.go` (255) | `TestOllamaSeamFailsClosed`, `TestOllamaRemoveReadsNotFoundAsDone`, `TestOllamaTagsReadsTheNameColumn`, `TestRealOllamaCLIPinsTheDaemon`, `TestOllamaFailureIsOneCleanLine`, `TestOllamaRemoveNotFoundMustNameTheTag`, `TestOllamaTimeoutIsSaidAsOne`, `TestRealOllamaCLITimeoutIsNotHeldByAChild` | git mv, unchanged |
| `cloudsync_picker_test.go` (407) | `TestThePickerShowsThePriceOpenRouterChargesNow` (reads `../../internal/cloudsync/testdata/openrouter_models.json`, same depth from `cmd/wt-model`) | git mv, unchanged |
| `cloudsync_removals_test.go` (397) | `TestApprovedDigests`, `TestOpenRouterRemovalNeedsApprovalUnderYes`, `TestOpenRouterMassRemovalRefusedWithoutForce`, `TestInteractiveDeclineRemovesNothing`, `TestYesWithOneFlowsDigestAppliesOnlyThatFlow`, `TestDryRunPrintsCombinedApproveLine`, `TestApproveAndForceAreAcceptedWithOnlyOpenrouter`, `TestRemovalAloneIsNotAdviceAboutRefreshing`, `TestWithheldRemovalsAreSaid`, `TestRemovalGate*` (3), `TestCloudSyncKeptVariant*` (2) | git mv; line 217 `&app{…}` → `&host{…}` |
| `host_commands_test.go` | `TestHostCommandsStillLoadTheRegistry` rows `cloud-sync --dry-run` (:112-126) and `broken registry/cloud-sync` (:206) | rows move to the table Task 42 (4a-iii) put in `cmd/wt-model/host_test.go`; the file stops depending on `cloudSyncHome`/`cloudSyncRegistry`/`stubCloudFetch`/`openRouterBody` |
| `testmain_test.go:104-116` | the three cloud-sync fail-closed stubs | move to `cmd/wt-model/testmain_test.go` |
| `internal/agents/price_notice_test.go:26,27,29,46`, `internal/tui/models_client_test.go:636`, `cmd/wt-session/stats_remote_test.go:202,301` | notice text | expected text changes in Task 46 |

### Task 45: `wt-model cloud-sync`, and `wt cloud-sync` becomes a pointer

**Files:**
- Move (git mv): `wt/cmd/wt-session/cloudsync.go` (684 lines), `cloudsync_catalog.go` (250), `cloudsync_ollama.go` (158) → `wt/cmd/wt-model/`; the six test files above → `wt/cmd/wt-model/`.
- Modify: `wt/cmd/wt-model/cloudsync.go` (:178 `cloudSyncCmd(a *app)`, :186-224 Long/Example, :244-247 the config read, :103-117 `promptCloudSync`); `wt/cmd/wt-model/main.go` (`rootCmd`: `root.AddCommand(...)`); `wt/cmd/wt-model/testmain_test.go`; `wt/cmd/wt-model/host_test.go` (Task 42's, 4a-iii); `wt/cmd/wt-session/main.go:641` (drop `cloudSyncCmd(a)`) and `:272-273` (Example); `wt/cmd/wt-session/moved.go` (`movedCommands`); `wt/cmd/wt-session/testmain_test.go:104-116`; `wt/cmd/wt-session/host_commands_test.go` (:14 import, :19-23 `hostHome`, :56-66 comment, :112-126, :149-150, :206, :211); `wt/cmd/wt-session/app.go:219` and `wt/cmd/wt-session/boundary_test.go:166-167` (comments: drop `cloud-sync` from the host-command lists); `wt/cmd/wt-session/model.go`, `wt/cmd/wt-session/model_test.go` (or `host_fixtures_test.go` if Task 38 renamed it) and `wt/cmd/wt-session/testmain_test.go:99-100` (the dead after-write sync seams, Step 3.9a); `wt/internal/removal/deletionscan_test.go:34`.
- Test: `wt/cmd/wt-model/cloudsync_test.go`, `wt/cmd/wt-model/host_test.go`, `wt/cmd/wt-session/moved_test.go`, `wt/internal/removal/deletionscan_test.go`.

**Interfaces:**
- Consumes: `type host struct` + `func (h *host) hostConfig() (*config.Config, error)` (cmd/wt-model/host.go, 3b); `cli.AskOnTTY(open func() (*os.File, error), question string, noTTY error) (bool, error)` (4a-i); `var openTTY func() (*os.File, error)`, `var syncRoutesAfterWrite func(out, errOut io.Writer) string` in cmd/wt-model (4a-ii); `config.ConfigError(err error) error` (`internal/config/config.go:1227`); `type movedCommand struct{ Old, New string }`, `var movedCommands []movedCommand`, `addMovedStubs` (cmd/wt-session/moved.go, 4a-i).
- Produces (cmd/wt-model, package main): `func cloudSyncCmd(h *host) *cobra.Command`; unchanged and now in cmd/wt-model: `func runCloudSync(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts) error`, `type cloudSyncOpts`, `func selectFlows(only string, given bool, o *cloudSyncOpts) error`, `func promptCloudSync(question string) (bool, error)`, the seams `cloudFetch`, `confirmCloudSync`, `cloudSyncNow`, `ollamaCLI`, `ollamaWaitDelay`, `func withRegistryHint(err error) error`. cmd/wt-session: `movedCommand{Old: "cloud-sync", New: "cloud-sync"}`.

- [ ] **Step 1: Move the tests and write the failing ones.**
  1. Before moving, list what an earlier PR already defined in cmd/wt-model under a name the moved files define: `cd wt && grep -nE '^func (withRegistryHint|prefixLines|promptCloudSync|graduatedClause|cloudSyncHome|stubCloudFetch|stubConfirm|runCS|noTerminal)\(|^(var|const) (cloudSyncRegistry|openRouterBody|sameOpenRouterBody|cloudHTTP)\b' cmd/wt-model/*.go`. Each hit is a copy a previous PR made for its own use (4a-ii's `model_init.go` — `runModelInit`, from `cmd/wt-session/model.go:137` — needs `withRegistryHint`); delete that earlier copy in this step (the moved definition is the one that stays, same text), so the package keeps exactly one.
  2. `git mv cmd/wt-session/cloudsync_test.go cmd/wt-session/cloudsync_catalog_test.go cmd/wt-session/cloudsync_flownames_test.go cmd/wt-session/cloudsync_ollama_test.go cmd/wt-session/cloudsync_picker_test.go cmd/wt-session/cloudsync_removals_test.go cmd/wt-model/`. In them: every `&app{hostSet: true, hostCfg: cfg}` → `&host{hostSet: true, hostCfg: cfg}` (flownames :28, :105; catalog :583; removals :217; cloudsync_test :287), `&app{hostSet: true, hostCfg: &config.Config{}, hostLoadErr: …}` → `&host{…}` (:307), `run := func(a *app, args ...string) error` → `func(h *host, …)` with `cloudSyncCmd(h)` (:280-281); the `t.Errorf` labels `"wt cloud-sync …"` → `"wt-model cloud-sync …"` (cloudsync_test :303, flownames :44, removals :222).
  3. Append to `cmd/wt-model/cloudsync_test.go`:
  ```go
  // TestWtModelServesCloudSync pins the move (phase 4 stage 2, PR 4b): wt-model's
  // root registers cloud-sync, visible in help, with every flag it had in wt,
  // and its help names the binary that runs it. A flag lost in the move would
  // turn a reviewed `--approve-removals` run into a usage error.
  func TestWtModelServesCloudSync(t *testing.T) {
  	cmd, _, err := rootCmd(fakeDeps).Find([]string{"cloud-sync"})   // build root as serve_test.go does at entry
  	// err == nil, cmd.Name() == "cloud-sync", !cmd.Hidden
  	// for each of "only", "dry-run", "yes", "html", "approve-removals", "force": cmd.Flags().Lookup(name) != nil
  	// cmd.Example == "  wt-model cloud-sync --dry-run\n  wt-model cloud-sync --yes --approve-removals 0bcc560b956e\n  wt-model cloud-sync --only openrouter --yes"
  	// strings.Contains(cmd.Long, "`wt-model model add` is marked manual and kept until its source lists it.")
  }

  // TestCloudSyncExitLinesNameWtModel pins the last stderr line a script and the
  // wt-cloud-sync skill branch on, as wt-model's main prints it: the codes are
  // unchanged by the move and the prefix is the binary's own.
  func TestCloudSyncExitLinesNameWtModel(t *testing.T) {
  	// var b bytes.Buffer; exitcode.Report("wt-model", flowOutcome{code: 3}.exitError(flowOllama), &b) == 3,
  	//   b.String() == "wt-model: cloud-sync: the ollama flow changed nothing: the pricing page changed shape\n"
  	// exitcode.Report("wt-model", flowOutcome{code: 5}.exitError(flowOpenRouter), &b) == 5,
  	//   "wt-model: cloud-sync: the openrouter flow changed nothing: removals not approved\n"
  	// exitcode.Report("wt-model", (&cloudSyncOutcome{failed: true}).err(), &b) == 1,
  	//   "wt-model: cloud-sync: a step failed; see the error lines above\n"
  }
  ```
  4. `cmd/wt-model/testmain_test.go`: after `lifecycle.IsolateProcessesForTest()`, add the three fail-closed stubs copied verbatim from `cmd/wt-session/testmain_test.go:104-116` (`cloudFetch` → `errors.New("cloudFetch not stubbed in this test: " + url)`, `ollamaCLI` → `"ollamaCLI not stubbed in this test: ollama " + strings.Join(args, " ")`, `confirmCloudSync` → `false, errors.New("confirmCloudSync not stubbed in this test")`) with their comment, `cmd/wt-session` → `cmd/wt-model`. `TestCloudSyncSeamsFailClosed` and `TestOllamaSeamFailsClosed` (moved) pin them.
  5. `cmd/wt-model/host_test.go`: add to `TestHostCommandsStillLoadTheRegistry` (created by Task 42, 4a-iii, with the served/warm rows) the row `{name: "cloud-sync --dry-run", build: cloudSyncCmd, args: []string{"--only", "openrouter", "--dry-run"}, before: …, check: …}` copied from `cmd/wt-session/host_commands_test.go:112-126` (before: `selectFlows("openrouter", true, &o)` then `runCloudSync`; check: output has prefix `"openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry"`) and the broken-registry row `{"cloud-sync", cloudSyncCmd, []string{"--dry-run"}}` (want `config.ConfigError(loadErr)`); the case loop gets `stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})` as wt-session's has at :150.
  6. `cmd/wt-session/moved_test.go`: in `TestMovedCommandsPointAtWtModel`, add one argument row beside the table walk (the list of explicit `args`/`want` rows 4a-i made for the conditional pointers): args `{"cloud-sync", "--only", "ollama", "--yes", "--approve-removals", "0bcc560b956e"}`, want error text `cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed`, `exitcode.Of(err) == 1`, one output line, config.toml and registry.toml bytes unchanged (it proves the stub takes cloud-sync's own flags unparsed).

- [ ] **Step 2: Run them to see them fail.**
  `cd wt && go vet ./cmd/wt-model` → `undefined: cloudSyncCmd`, `undefined: runCloudSync`, `undefined: cloudFetch` (the source files have not moved). `go vet ./cmd/wt-session` → `undefined: cloudSyncRegistry`, `undefined: cloudSyncHome`, `undefined: stubCloudFetch` in host_commands_test.go (fixed in Step 3).

- [ ] **Step 3: Move the command and point the old name.**
  1. `git mv cmd/wt-session/cloudsync.go cmd/wt-session/cloudsync_catalog.go cmd/wt-session/cloudsync_ollama.go cmd/wt-model/`. File headers: `wt cloud-sync` → `wt-model cloud-sync`, `cmd/wt-session's TestMain` → `cmd/wt-model's TestMain` (cloudsync.go:25, cloudsync_ollama.go:19).
  2. `cloudsync.go:178`: `func cloudSyncCmd(h *host) *cobra.Command`; :244-247 `cfg, loadErr := h.hostConfig(); if loadErr != nil { return config.ConfigError(loadErr) }`.
  3. `cloudsync.go:200-201` Long: `` "`wt model add` is marked manual" `` → `` "`wt-model model add` is marked manual" `` (rewrap the two lines to keep 80 columns: `"name both, comma-separated, when both flows remove. A model added with\n" + "`wt-model model add` is marked manual and kept until its source lists it.\n"`); :222-224 Example: the three lines with `wt-model cloud-sync` (exact text in Step 1's test).
  4. `promptCloudSync` (:107): body is one line, `return cli.AskOnTTY(openTTY, question, errors.New("there is no terminal to confirm on — rerun with --yes to apply without asking"))` (if 4a-i already made it so in wt-session, it moves as is). `askYesNo` is then not referenced by the moved files.
  5. `cmd/wt-model/main.go` `rootCmd`: add `cloudSyncCmd(h)` to `root.AddCommand(...)` beside 4a-ii's `modelCmd(h)`, with the same `h`.
  6. `cmd/wt-session/main.go:641`: remove `cloudSyncCmd(a), `; :272-273: delete the `wt cloud-sync --dry-run` Example line and end :272 with `# smoke-test every agent that supports a model",`. `cmd/wt-session/moved.go`: append `{Old: "cloud-sync", New: "cloud-sync"}` to `movedCommands`.
  7. `cmd/wt-session/testmain_test.go`: delete :104-116 (the cloud-sync seams and their comment).
  8. `cmd/wt-session/host_commands_test.go`: delete the two cloud-sync rows (:112-126, :206) and :150 (`stubCloudFetch`), drop the `internal/cloudsync` import; add `const hostCommandsRegistry` = the text of `cloudSyncRegistry` (`cloudsync_test.go:35-97` on main), a test-only copy with a comment saying so (test files cannot cross packages); `hostHome` (:23) builds its home with `path := modelHome(t, registry+"\n[[providers]]\nid = \"agy\"…")` (no `cloudSyncHome`: the clock and TMPDIR it set were cloud-sync's); :149 and :211 pass `hostCommandsRegistry`; if the broken-registry loop has no row left, delete the loop; update the doc comments at :19 and :56-66 to the commands still listed.
  9. `app.go:219` and `boundary_test.go:166-167`: drop `cloud-sync` from the comment lists.
  9a. The after-write route sync loses its last wt-session caller here (cloudsync.go:606; `model_write.go` left in Task 38). Run `cd wt && grep -nw 'syncRoutesAfterWrite\|realSyncRoutesAfterWrite\|seedEnv\|stubSeedEnv\|stubRouteSync\|realRouteSync' cmd/wt-session/*.go`; delete each declaration that has no reader left besides its own definition, its `TestMain` stub (`testmain_test.go:99-100`) and its test helper (`model_test.go:20-50`), together with that stub and helper and their comments (`model.go:17-53` is expected to go entirely, leaving `modelCmd`). Then `probeInventory` is read only by `runLitellmSync` (Task 50 deletes both). Any other remaining reader stays; say which in the commit body.
  10. `internal/removal/deletionscan_test.go:34`: key `"cmd/wt-session/cloudsync_catalog.go"` → `"cmd/wt-model/cloudsync_catalog.go"`, count `1` and reason unchanged.
  11. `gofmt -w cmd/wt-model cmd/wt-session internal/removal`.

- [ ] **Step 4: Run the tests, expect PASS.**
  `cd wt && go build ./... && go vet ./... && go test ./cmd/wt-model -count=1 && go test ./internal/removal -run 'TestDeletionStringsOnlyInRemovalPackage|TestDeletionScanSeesEachShape' -count=1 && go test ./internal/depcheck -count=1 && go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestHostCommandsStillLoadTheRegistry|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestOneErrorLineThroughTheRealTree|TestConfigHomeIsNotTheDevelopersOwn|TestPriceNotice' -count=1 && make check`. Then the built binaries, off this machine:
  ```bash
  D=$(mktemp -d); go build -o "$D/wt" ./cmd/wt-session && go build -o "$D/wt-model" ./cmd/wt-model
  env -i PATH="$PATH" HOME="$D/home" WT_REGISTRY="$D/registry.toml" WT_LITELLM_CONFIG="$D/config.yaml" WT_LITELLM_RESTART_CMD=true "$D/wt" cloud-sync --dry-run; echo "exit $?"
  # → wt: cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed / exit 1
  env -i PATH="$PATH" HOME="$D/home" WT_REGISTRY="$D/registry.toml" WT_LITELLM_CONFIG="$D/config.yaml" WT_LITELLM_RESTART_CMD=true "$D/wt-model" cloud-sync --dry-run; echo "exit $?"
  # → one line starting "wt-model: config error: model registry not found" / exit 1 (no registry: nothing fetched)
  "$D/wt-model" cloud-sync --help | grep -c -e '^  openrouter  ' -e '^  ollama      '   # → 2
  ```

- [ ] **Step 5: Commit.** `git add -A wt/cmd/wt-model wt/cmd/wt-session wt/internal/removal/deletionscan_test.go` then
  `git commit -m "feat(wt-model)!: move cloud-sync to wt-model; wt cloud-sync points there" -m "Same flags, plans, prompts and exit codes 0-5; the error lines carry the wt-model prefix. wt cloud-sync prints the moved-to-wt-model pointer and exits 1." -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`.

### Task 46: Every Go string and comment names `wt-model cloud-sync`

**Files:**
- Modify: `wt/internal/agents/price_notice.go:12,18,23,28,33`; `wt/cmd/wt-model/cloudsync_catalog.go:100,235`; `wt/cmd/wt-model/model_write.go` (the `--source` flag help moved from `cmd/wt-session/model_write.go:278`: `"…manual keeps a cloud model through wt cloud-sync until…"`); comments in `wt/internal/cloudsync/doc.go:1,23`, `wt/internal/config/config.go:40,399,459,1046,1068,1094`, `wt/internal/config/price_at.go:54`, `wt/internal/provider/table.go:16`, `wt/internal/exitcode/exitcode.go:13`, `wt/internal/modeladmin/apply.go:376`, `wt/internal/litellm/restart.go:442`, `wt/internal/modelserver/deps.go:37`, and every test comment `git grep` finds.
- Test: `wt/internal/agents/price_notice_test.go:26,27,29,46`, `wt/internal/tui/models_client_test.go:636`, `wt/cmd/wt-session/stats_remote_test.go:202,301`, `wt/cmd/wt-model/cloudsync_catalog_test.go:498`.

**Interfaces:**
- Consumes: Task 45's `cmd/wt-model` files.
- Produces: `agents.PriceNotice(last time.Time, priced bool, now time.Time) string` (signature unchanged) returning `wt: token pricing last refreshed <YYYY-MM-DD> — run 'wt-model cloud-sync'` and `wt: token pricing has never been refreshed — run 'wt-model cloud-sync'`; the ollama retry line `ollama:   <tag> is in the registry but not pulled, so it is not routed; run `+"`wt-model cloud-sync`"+` again to retry the pull`.

- [ ] **Step 1: Write the failing tests.** Change only the expected strings: `price_notice_test.go:26` → `"wt: token pricing last refreshed 2026-09-30 — run 'wt-model cloud-sync'"`, :27 → `"… 2026-09-01 — run 'wt-model cloud-sync'"`, :29 → `"wt: token pricing has never been refreshed — run 'wt-model cloud-sync'"`, :46 → `"… 2026-10-03 — run 'wt-model cloud-sync'"`; `models_client_test.go:636` → `"\nsummary line\nwt: token pricing last refreshed 2026-11-01 — run 'wt-model cloud-sync'\n"`; `stats_remote_test.go:202` → `"wt: token pricing has never been refreshed — run 'wt-model cloud-sync'\n"`, :301 suffix `"run 'wt-model cloud-sync'"`; `cloudsync_catalog_test.go:498` → `` "ollama:   gemma4:cloud is in the registry but not pulled, so it is not routed; run `wt-model cloud-sync` again to retry the pull\n" ``. (`TestPriceNoticeFromCatalog`'s `strings.Count(got, "cloud-sync") != 1` stays true.)
- [ ] **Step 2: Run them to see them fail.** `cd wt && go test ./internal/agents -run TestPriceNotice -count=1` → FAIL, got `… run 'wt cloud-sync'`; `go test ./internal/tui -run TestPriceNotice -count=1`, `go test ./cmd/wt-model -run TestCloudSyncOllamaCLIFailuresExit1AndTheRestStillRuns -count=1` → FAIL the same way.
- [ ] **Step 3: Implement.** `price_notice.go:23,28`: `'wt cloud-sync'` → `'wt-model cloud-sync'`; `cloudsync_catalog.go:235`: `` `wt cloud-sync` `` → `` `wt-model cloud-sync` ``; `model_write.go` flag help → `"manual or curated: manual keeps a cloud model through wt-model cloud-sync until its source lists it"`. Then the comments, mechanically: `git grep -l "wt cloud-sync" -- wt/cmd wt/internal | xargs sed -i '' 's/wt cloud-sync/wt-model cloud-sync/g'` followed by `gofmt -w wt/cmd wt/internal` and a read of `git diff --stat` (only comment lines and the strings above may change).
- [ ] **Step 4: Run the tests, expect PASS.** `cd wt && go build ./... && go vet ./... && make check && go test ./internal/agents ./internal/tui ./internal/config ./internal/cloudsync ./cmd/wt-model -count=1 && go test ./cmd/wt-session -run 'TestPriceNotice|TestRemoteLaunchPrintsThePriceNoticeAfterTheSummary' -count=1`; `git grep -n "wt cloud-sync" -- wt/cmd wt/internal` prints nothing.
- [ ] **Step 5: Commit.** `git add -u wt/internal wt/cmd` then `git commit -m "feat(wt): the stale-pricing notice and cloud-sync's hints name wt-model cloud-sync" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`.

### Task 47: The skills run `wt-model cloud-sync`

**Files:**
- Modify: `wt/.claude/skills/wt-cloud-sync/SKILL.md` (:3 description, :8, :15-16, :27, :55-60, :187, :224-226, :239, :268, :323, and every other `wt cloud-sync`); `.claude/skills/wt-manage-slot-profiles/SKILL.md:12,141`.
- Test: `make lint-skills` (root), the grep below.

**Interfaces:** Consumes the command surface of Task 45 and the texts of Task 46 (the skill quotes them; the repo rule "change the page and the skill with the behavior", style guide D3).

- [ ] **Step 1: The failing check.** `git grep -n -e "wt cloud-sync" -e "wt: cloud-sync:" -e "cmd/wt-session cloud-sync" -- wt/.claude/skills .claude/skills` → lists the lines above (non-empty).
- [ ] **Step 2: Run it to see it fail.** The command in Step 1 prints ≥ 16 lines.
- [ ] **Step 3: Edit.** In `wt-cloud-sync/SKILL.md`:
  - Front matter `description` (keep `name: wt-cloud-sync`): `` …with `wt-model cloud-sync` — … when wt prints "token pricing last refreshed … — run 'wt-model cloud-sync'", or when `wt-model cloud-sync` exits 3 (the ollama pricing page changed shape and the parser needs repair). `` — the exit-3 trigger now names the new binary; add, in the body after the opening paragraph, one line: `` `cloud-sync` run under `wt` (its old home) only prints `wt: cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed` and exits 1, never 2 to 5: run `wt-model cloud-sync`; that exit 1 is not the parser case. ``
  - Every command line `wt cloud-sync …` → `wt-model cloud-sync …`; the notice quote at :15-16 → `` `token pricing last refreshed <date> — run 'wt-model cloud-sync'` ``.
  - Step 1 (:56-60): `` First run `wt-model cloud-sync --help`. If `wt-model` is not on PATH, or it does not print the two flows (`openrouter`, `ollama`), the installed `wt-model` predates the command: stop and ask the user to run `make install` from the repo root. ``
  - Exit codes (:224-226): `` `wt-model: cloud-sync: the ollama flow changed nothing: <why>` `` and `` `wt-model: cloud-sync: a step failed; see the error lines above` ``; :239 `Unlike the other wt and wt-model commands, cloud-sync does not use 3 for approval missing …`.
  - :268 the retry line as Task 46 prints it; :323 `` `go run ./cmd/wt-model cloud-sync --only ollama --dry-run --html <saved page>` ``.
  In `wt-manage-slot-profiles/SKILL.md:12,141`: `` `wt cloud-sync` `` → `` `wt-model cloud-sync` ``.
- [ ] **Step 4: Run the checks, expect PASS.** The Step 1 grep prints nothing; from the repo root `make lint-skills && make check-links`.
- [ ] **Step 5: Commit.** `git add wt/.claude/skills/wt-cloud-sync/SKILL.md .claude/skills/wt-manage-slot-profiles/SKILL.md` then `git commit -m "docs(skills): wt-cloud-sync and wt-manage-slot-profiles run wt-model cloud-sync" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`.

### Task 48: Pages, guides, CLAUDE.md and the changelog

**Files:**
- Modify: `wt/docs/wt-cloud-sync.md` (title :1, :9-12, :553-561, :647, :666, :818-830 and every `wt cloud-sync`); `wt/docs/internals/cloud-sync.md:3,29`; `wt/docs/internals/packages.md:34-36` (rows → `cmd/wt-model/cloudsync.go`, `cloudsync_catalog.go`, `cloudsync_ollama.go`, describing `wt-model cloud-sync`; placed with the other `cmd/wt-model` rows); `wt/docs/internals/testing.md:94`, `wt/docs/internals/launch-flow.md:47`, `wt/docs/internals/config-and-registry.md` (3 mentions), `wt/docs/internals/tui.md:27`, `wt/docs/internals/local-models.md:35`; `wt/README.md:110` (the command table row: `wt-model cloud-sync`); `wt/docs/wt-model.md:74,165,264`, `wt/docs/wt-session.md:239`, `wt/docs/wt-model-server.md:352`, `wt/docs/wt-agents/README.md:57`, `wt/docs/wt-agents/slot-profiles.md:181`; guides `docs/guides/00-config-map.md:15,47`, `02-providers-and-models.md:40,66,68,104,350`, `04-litellm-config.md:185`, `05-benchmarks.md:208`, `06-wt-agents-and-models.md:73`, `08-maintenance-and-troubleshooting.md:353,491,498`, `13-deployment-use-cases.md:82,124`, `14-litellm-install-and-configuration.md:74`; `CLAUDE.md:25,48`; `wt/CLAUDE.md:218-226,326,343`; `wt/CHANGELOG.md` (`## Unreleased` → `### Changed`).
- Test: the grep gate; `make check-links`.

**Interfaces:** Consumes Task 45–47's names and texts; produces none.

- [ ] **Step 1: The failing check.** `git grep -n -e "wt cloud-sync" -e "wt: cloud-sync:" -e "cmd/wt-session/cloudsync" -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` → non-empty.
- [ ] **Step 2: Run it to see it fail.** It lists the files above.
- [ ] **Step 3: Edit.**
  - Every `wt cloud-sync` → `wt-model cloud-sync`; `wt: cloud-sync:` → `wt-model: cloud-sync:`; `cmd/wt-session/cloudsync*.go` → `cmd/wt-model/cloudsync*.go`; `cmd/wt-session's TestMain` (where it names cloud-sync's seams) → `cmd/wt-model's TestMain`. Keep whatever 4a made of other names on the same lines.
  - `wt/docs/wt-cloud-sync.md`: title `` # `wt-model cloud-sync` ``; after the opening code block add: `` `cloud-sync` run under `wt` (its home before phase 4 stage 2) prints `wt: cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed` and exits 1; it changes nothing and never exits 2 to 5. Run it where `wt-model serve` runs: it writes that machine's `registry.toml`, ollama and LiteLLM routes. ``; exit section :553: `cloud-sync is a named exception to the exit table the other wt and wt-model commands share …`. The file name stays (links point at it).
  - `docs/guides/02-providers-and-models.md:40`: move `` `wt-model cloud-sync` (OpenRouter prices, and ollama's cloud models with their prices) `` out of the **wt** clause into the clause naming wt-model's commands (the one 4a-ii wrote; if none, add `**wt-model** = …` after the **wt** clause). `08-maintenance-and-troubleshooting.md:491`: `| `refresh-prices`, `ollama-catalog sync` | `wt-model cloud-sync` |`; :498: name `wt-model cloud-sync` among the host commands (no other change to the interim-limit sentence; 5b rewrites it).
  - `CLAUDE.md:25`: `` - `wt-model cloud-sync [--only openrouter,ollama] [--dry-run] [--yes --approve-removals DIGEST[,DIGEST]] [--force] [--html FILE]` — … `` (rest unchanged, plus `` `cloud-sync` under `wt` is a pointer, exit 1 ``); :48 the writer list names `wt-model cloud-sync`.
  - `wt/CLAUDE.md:218`: heading `## Cloud sync (`wt-model cloud-sync`)`; :220 `` `cmd/wt-model/cloudsync*.go` owns fetches, … `cloud-sync` under `wt` is a pointer stub (`cmd/wt-session/moved.go`, exit 1). ``; :326 `wt-model cloud-sync --dry-run   # plan a price and ollama-catalog refresh; changes nothing`; :343 `` `wt-model cloud-sync` still runs the real `ollama` ``.
  - `wt/CHANGELOG.md`, first bullet under `## Unreleased` / `### Changed`: `` - **Breaking:** `wt cloud-sync` moved to **`wt-model cloud-sync`** with the same flags (`--only`, `--dry-run`, `--yes`, `--approve-removals`, `--force`, `--html`), plans, question and exit codes 0 to 5; its error lines now start `wt-model: `. `wt cloud-sync` prints `wt: cloud-sync moved to wt-model — run 'wt-model cloud-sync'; nothing was changed` and exits 1. The stale-pricing notice says `run 'wt-model cloud-sync'`; the `wt-cloud-sync` skill runs the new name. ``
  - Guides show no live model state; home paths, if any appear, are `/Users/user_name`.
- [ ] **Step 4: Run the checks, expect PASS.** The Step 1 grep prints nothing; from the repo root `make lint` (lint-shell, lint-skills, check-links).
- [ ] **Step 5: Commit.** `git add -u docs wt/docs wt/README.md wt/CLAUDE.md wt/CHANGELOG.md CLAUDE.md` then `git commit -m "docs(wt): cloud-sync pages, guides and CLAUDE.md name wt-model cloud-sync" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`.

PR gate (after Task 48): the Exit gate above, the full `go test ./...` in the background (`cmd/wt-session` ~3.5 min, `internal/removal` ~6 min), and the binary checks of Task 45 Step 4. PR body: "Phase 4 stage 2, PR 4b (cloud-sync to wt-model). Refs #N" with the tracking issue number, never a closing keyword.

---

## PR 4c: litellm sync|list|providers|restart move to wt-model

**Entry:** PR 4b merged (so 3a's `internal/litellm/syncinputs.go` + `report.go` — `litellm.RunSync`, `litellm.Report`, `litellm.ReportSyncPlan`, `litellm.ErrIDFailed`, `litellm.Probe` — exist; 3b's `cmd/wt-model/host.go` `*host` with its `hostConfig()` registry read exists; 4a-i's `cmd/wt-session/moved.go` (`movedCommands`, `movedStub`, `addMovedStubs`, `movedError`) and `TestMovedCommandsPointAtWtModel` exist; 4a-ii's `cmd/wt-model` `probeInventory` seam, `stubProbeInventory`, `modelHome`, `withCleanConfigEnv`, `writeEmptyRegistry`, `jsonKeySet` and `load_error_hint_test.go` helpers exist). `wt litellm sync|list|providers|restart` still run in `cmd/wt-session/litellm.go`.
**Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1` (run in the background, ~10 min; `cmd/wt-session` ~3.5 min), with `go test ./internal/depcheck ./internal/removal -count=1` named in the PR body; from the repo root `make lint-shell && make check-links`; `git grep -nE "wt litellm (sync|list|providers|restart)" -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` prints nothing (the same exclusion set as Task 75; `:!docs/superpowers` does not cover `wt/docs/superpowers`, whose dated 2026-09-25 plan names the old command).
**Size:** ~1,000 changed + ~2,000 moved. The move is recorded as renames: Task 50 `git mv`s `litellm_test.go` to `cmd/wt-model/litellm_sync_test.go` and recreates a small wt-session `litellm_test.go` (status/set/on|off/#179 and, until Task 51, restart), and `litellm_dup_test.go` is a git mv; with rename detection the reviewed diff is about 1,500 lines. If `git diff --stat -M origin/main` shows the sync tests as delete + add (similarity below 50%), split the PR after Task 50: 4c-i (Tasks 49–50, exit gate = the Go gate only) and 4c-ii (Tasks 51–53, the full exit gate above, old-name grep included); task order is unchanged.

Line numbers below are `origin/main` (f60ad48a). At entry, PR 3a has already cut `cmd/wt-session/litellm.go:23-555` (report types, `syncInputs` and its helpers) into `internal/litellm`, so the functions this PR touches are found by name: `litellmCmd` (today :628-712), `runLitellmList` (:558-587), `runLitellmProviders` (:589-607), `runLitellmRestart` (:760-784), `removedLitellmCmd` (:747-758), the sync `RunE` (:636-642). A step that says "the tests named X" means those of the names listed that are still in the file at entry; the classification rule is fixed: **a test of `status`, `set`, `on|off`, the `[litellm] enabled` note or the #179 `expose|unexpose` stubs stays in `cmd/wt-session`; every other test in `cmd/wt-session/litellm_test.go` and `litellm_dup_test.go` moves to `cmd/wt-model`.** Before Task 49 run `cd wt && grep -n '^func Test' cmd/wt-session/litellm_test.go cmd/wt-session/litellm_dup_test.go` and check every name against the lists below.

The moved commands keep their flags (`--json`, `--dry-run` on sync), output shapes, JSON (fixture `docs/contracts/litellm-cli.sample.json`, unchanged) and exit codes. `cmd/wt-model/litellm.go`'s `status`/`set` twins are untouched.

---

### Task 49: `wt-model litellm list|providers`; `wt litellm list|providers` become pointers

**Files:**
- Modify `wt/cmd/wt-model/litellm.go` (whole file, 112 lines): add `runLitellmList`, `runLitellmProviders`, the `list` and `providers` subcommands; header comment and `litellmCmd`'s `Short` updated.
- Modify `wt/cmd/wt-session/litellm.go`: delete `runLitellmList` (:558-587), `runLitellmProviders` (:589-607), `listC`/`provC` and their flag vars (:633, :646-655), and their entries in `c.AddCommand` (:707); drop `sort` and `encoding/json` from the imports if nothing else uses them.
- Modify `wt/cmd/wt-session/moved.go`: two rows in `movedCommands`.
- Modify `wt/cmd/wt-session/host_commands_test.go`: delete the `litellm list` row (:127), the `noRegistry` field (:83-85) and its check `!tc.noRegistry &&` (:189), and `litellm list` from the doc comment (:58).
- Test: `wt/cmd/wt-model/litellm_test.go` (append), `wt/cmd/wt-session/litellm_test.go` (delete the moved tests).

**Interfaces:**
- Consumes: `litellm.Open(path string) (*litellm.File, error)`, `litellm.DefaultPath() string`, `(*litellm.File).Rows() []litellm.RowInfo`, `litellm.Providers() map[string]bool`; `movedCommand{Old, New string}` (spine §10).
- Produces (package `main` of `cmd/wt-model`): `func runLitellmList(out io.Writer, asJSON bool) error` and `func runLitellmProviders(out io.Writer, asJSON bool) error` (bodies moved verbatim from `cmd/wt-session/litellm.go:558-607`); test helper `func litellmEnv(t *testing.T, body string) string` (copied verbatim from `cmd/wt-session/litellm_test.go:63-72`; the wt-session copy stays, it is still used there).

- [ ] **Step 1: Write the failing tests.**
  - In `cmd/wt-session/moved.go` append to `movedCommands`: `{Old: "litellm list", New: "litellm list"}`, `{Old: "litellm providers", New: "litellm providers"}`. (This is the failing test for wt-session: `TestMovedCommandsPointAtWtModel` walks the table, and the real `list`/`providers` still win over the stubs.)
  - Move `TestLitellmListAndProviders` (:226-249), `TestLitellmListTextOutput` (:1034-1056) and `TestLitellmListJSONRows` (:1058-1076) verbatim from `cmd/wt-session/litellm_test.go` to `cmd/wt-model/litellm_test.go`, with `litellmEnv`; in their doc comments write `wt-model litellm list` for `wt litellm list`.
  - New in `cmd/wt-model/litellm_test.go`:
    ```go
    // TestLitellmListReadsNoRegistry pins that `wt-model litellm list` and
    // `providers` read only config.yaml and the provider policy table: a
    // registry.toml that does not parse must not stop the one read-only answer
    // to "is this model routed?" (they never had a config gate in wt either).
    func TestLitellmListReadsNoRegistry(t *testing.T) {
        home := proxyEnv(t)
        reg := filepath.Join(home, "local-ai", "registry.toml")
        // MkdirAll + WriteFile(reg, "models = [\n", 0o600)
        litellmEnv(t, "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n")
        stdout, stderr, err := execLitellm(t, "list")
        // want: err == nil, stdout == "a/b\t(hand-written)\n", stderr == ""
        stdout, _, err = execLitellm(t, "providers", "--json")
        // want: err == nil; json.Unmarshal into struct{ Providers map[string]struct{ Cloud bool } `json:"providers"` }
        //       Providers["openrouter"].Cloud == true, Providers["ollama"].Cloud == false
    }
    ```
- [ ] **Step 2: Run them to see them fail.** `cd wt && go test ./cmd/wt-model -run 'TestLitellmList|TestLitellmProviders' -count=1` → compile error `undefined: runLitellmList` / `runLitellmProviders`. `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel' -count=1` → panic `moved command litellm list is still registered` while the root is built (Task 36's `addMovedStubs`), so every wt-session test panics until Step 3 removes the real `list`/`providers`.
- [ ] **Step 3: Implement.**
  - `cmd/wt-model/litellm.go`: move `runLitellmList` and `runLitellmProviders` verbatim; in `litellmCmd` add `listC` (`Use: "list"`, `Short: "List routed model ids from config.yaml"`, `Args: cobra.NoArgs`, `SilenceUsage: true`, `--json` "print one JSON document (docs/contracts/litellm-cli.sample.json)") and `provC` (`Use: "providers"`, `Short: "List providers with a LiteLLM mapping"`, same `--json`), each with a `Long` in the twins' shape: one paragraph (list: "Read LiteLLM's config.yaml (WT_LITELLM_CONFIG, default ~/.config/litellm/config.yaml) and print one routed model id per line; a row wt did not write is printed as `<id><TAB>(hand-written)`. Reads no registry."; providers: "Print every provider id wt has a LiteLLM mapping for and whether it is a cloud provider. Reads no registry."), then `Flags:` and `Exit codes:\n  0  done\n  1  any failure`. `c.AddCommand(statusC, setC, listC, provC)`. `litellmCmd`'s `Short` becomes `"Manage LiteLLM routes for registry models, and the proxy url and key"`; the file's header comment says the file holds the route commands moved from `wt litellm` in phase 4 stage 2 beside the `set|status` twins.
  - `cmd/wt-session/litellm.go`: the deletions listed under **Files**.
  - `cmd/wt-session/host_commands_test.go`: the deletions listed under **Files**.
- [ ] **Step 4: Run the tests, expect PASS.** `cd wt && go test ./cmd/wt-model -count=1` and `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestHostCommandsStillLoadTheRegistry|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestLitellm' -count=1`, then `go vet ./cmd/... && gofmt -l cmd`.
- [ ] **Step 5: Commit.** `git add wt/cmd/wt-model/litellm.go wt/cmd/wt-model/litellm_test.go wt/cmd/wt-session/litellm.go wt/cmd/wt-session/litellm_test.go wt/cmd/wt-session/moved.go wt/cmd/wt-session/host_commands_test.go` — `feat(wt-model): move litellm list and providers to wt-model; wt litellm list|providers point there`

---

### Task 50: `wt-model litellm sync`; `wt litellm sync` becomes a pointer

**Files:**
- Modify `wt/cmd/wt-model/litellm.go`: `litellmCmd(h *host)`, the `sync` subcommand, `runLitellmSync`.
- Modify `wt/cmd/wt-model/main.go:34`: `litellmCmd()` → `litellmCmd(h)`, `h` being the `*host` `rootCmd` already builds for `modelCmd` (PR 3b/4a-ii).
- Modify `wt/cmd/wt-session/litellm.go`: delete `syncC`, `syncJSON`, `syncDryRun` (:633-645) and `syncC` in `c.AddCommand`; delete wt-session's sync wrapper (`runLitellmSync`, if PR 3a kept one); `removedLitellmCmd`'s error (:755) and doc comment (:740-746) name `wt-model litellm sync`.
- Modify `wt/cmd/wt-session/moved.go`: one row.
- Modify `wt/cmd/wt-session/testmain_test.go:54,136-141`, and `wt/cmd/wt-session/litellm.go` (where Task 37 put wt-session's `probeInventory` declaration): see Step 3, last bullet.
- Modify comments: `wt/cmd/wt-session/main.go:44-48` and `wt/cmd/wt-session/boundary_test.go:166` (the host route writers are now `wt stop <provider>` and `--all` only; drop `litellm sync` and, if still listed, `cloud-sync`).
- Move: `git mv wt/cmd/wt-session/litellm_dup_test.go wt/cmd/wt-model/litellm_dup_test.go`.
- Move: `TestLitellmLoadErrorNamesTheRepairThatWorks` (`wt/cmd/wt-session/load_error_hint_test.go:153-177`) → `wt/cmd/wt-model/load_error_hint_test.go`.
- Move (rename, so the PR diff records the bulk as one): `git mv wt/cmd/wt-session/litellm_test.go wt/cmd/wt-model/litellm_sync_test.go`, then recreate a small `wt/cmd/wt-session/litellm_test.go` (Step 1, "Mechanics").
- Test: `wt/cmd/wt-model/litellm_sync_test.go` (the rename), `wt/cmd/wt-session/litellm_test.go` (recreated: only what stays; one assertion change).

**Interfaces:**
- Consumes: `litellm.RunSync(out, errOut io.Writer, cfg *config.Config, probe litellm.Probe, asJSON, dryRun bool) error`, `litellm.Report`, `litellm.ReportSyncPlan`, `litellm.ErrIDFailed` (spine §9, PR 3a); `(*host).hostConfig() (*config.Config, error)` (PR 3b, from `app.go:226`); `config.ConfigError(err error) error` (`internal/config/config.go:1227`); cmd/wt-model's `probeInventory` package var and `stubProbeInventory(t, snap)` (PR 4a-ii).
- Produces (cmd/wt-model): `func litellmCmd(h *host) *cobra.Command`; `func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error` (one line: `return litellm.RunSync(out, errOut, cfg, probeInventory, asJSON, dryRun)`; the moved tests call it by this name); test helpers `routeRows`, `litellmTestConfig`, `litellmCloudTestConfig`, `downProbeWarn` (verbatim from `cmd/wt-session/litellm_test.go:39-46, 48-59, 764-772, 955`), and `jsonKeySet` only if `grep -n 'func jsonKeySet' cmd/wt-model/*_test.go` finds none.

- [ ] **Step 1: Write the failing tests.**
  - **Mechanics** (keeps the PR near the ~1,500-line bar: `litellm_test.go` is 2,299 lines and a cut-and-paste would count it twice): `git mv cmd/wt-session/litellm_test.go cmd/wt-model/litellm_sync_test.go`. In the renamed file keep only the tests this task moves (the list below) and the helpers they use; delete every test that stays in wt-session by the 4c rule (status, set, on|off, the `[litellm] enabled` note, the #179 stubs — `TestLitellmRemovedExposeCommandsFailAndPointToSync`, `TestLitellmRoutingCommandsWorkOnValidationOnlyError`, `TestLitellmOnOffAreRetired`, `TestLitellmStatusHasNoEnabledKey` among them) and the `restart` tests Task 51 moves (restart still runs in wt-session until then); delete any helper `cmd/wt-model/*_test.go` already declares (`grep -n '^func \|^const \|^var ' cmd/wt-model/*_test.go`), so each name is declared once, and adapt the moved calls to wt-model's helper (`execLitellm(t, …)`). Then recreate `cmd/wt-session/litellm_test.go` from `git show HEAD:wt/cmd/wt-session/litellm_test.go`, keeping exactly the deleted wt-session tests, the restart tests and the helpers they use (file header and imports trimmed by `goimports`/the build). `git diff --stat -M HEAD` should show the rename with most lines unchanged.
  - `cmd/wt-session/moved.go`: append `{Old: "litellm sync", New: "litellm sync"}`.
  - `cmd/wt-session/litellm_test.go` `TestLitellmRemovedExposeCommandsFailAndPointToSync` (:554-581): the error assertion becomes `strings.Contains(err.Error(), "wt-model litellm sync")` (message text "want a removal error pointing to `wt-model litellm sync`"); doc comment says `wt-model litellm sync`.
  - Moved verbatim (by the rename above) into `cmd/wt-model/litellm_sync_test.go` (references to `wt litellm sync` in their comments become `wt-model litellm sync`; calls of `reportLitellm`/`reportSyncPlan`/`errLitellmIDFailed` are already `litellm.Report`/`litellm.ReportSyncPlan`/`litellm.ErrIDFailed` after PR 3a): `TestLitellmSyncUsesLiveInventory`, `TestLitellmSyncWaitsForTheProxy`, `TestLitellmSyncLeavesUnreachableFamilyAlone`, `TestLitellmSyncRemovesRoutesOfRefusedProvider`, `TestLitellmSyncNoWarningForUnprobedProvider`, `TestLitellmSyncDryRunJSON`, `TestLitellmSyncDryRunJSONMatchesContract`, `TestLitellmSyncJSONMatchesContract`, `TestLitellmSyncDryRunReportsProbeWarnings`, `TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant`, `TestSyncRoutesMlxLMServerByProbe`, `TestSyncRoutesMlxLMServerUnattributableAnswerLeavesRoutes`, `TestLitellmSyncRoutesDiscoveredModels`, `TestLitellmSyncDropsDiscoveredEntryCollidingWithRegistryID`, `TestStartHookAndSyncAgreeOnDiscoveredRoute`, `TestEnsureRouteChangeIsIdempotent`, `TestLitellmSyncRemovesRoutesOfUnprobedFamily`, `TestLitellmSyncKeepsRoutesOfRegistryGapFamily`, `TestLitellmSyncWarnsWhenAGapFamilyHasOnlyRegistryRoutes`, `TestLitellmSyncRemovesRoutesOfNonLocalFamily`, `TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily`, `TestLitellmSyncDryRunMatchesRealSyncForDiscovered`, `TestSyncAndStartHookAgreeWhenOmlxListsAnUnloadedSibling`, `TestSyncReportsTheAPIBaseRepairInText`, `TestLitellmSyncWarnsAboutAnOllamaServeRow`, `TestLitellmSyncWarnsAboutAProviderWithNoRow`; and any of `TestDesiredLocalIDsOllamaFollowsPulled`, `TestDesiredLocalIDsPulledNeedsTrustedProbe`, `TestUntrustedFamilies`, `TestSyncProbeWarningIgnoresHandWrittenRows`, `TestSyncProbeWarningCarriesTheProbeError`, `TestMissingProviderWarningsOrderAndTheLineAlreadySaid` still in `cmd/wt-session` at entry (expected: none; PR 3a moved them to `internal/litellm/syncinputs_test.go`).
  - Rewritten in `cmd/wt-model/litellm_sync_test.go` (they built an `*app` literal; here they go through the real tree and a real registry, so they do not depend on `host`'s fields):
    ```go
    // TestLitellmSyncRefusesOnConfigError pins that sync refuses when the
    // registry failed to LOAD, writing nothing: acting on an empty registry
    // would still write config.yaml and restart the proxy.
    func TestLitellmSyncRefusesOnConfigError(t *testing.T) {
        body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
        home := proxyEnv(t)
        // write home/local-ai/registry.toml = "models = [\n"
        p := litellmEnv(t, body)
        _, _, err := execLitellm(t, "sync")
        // want: err != nil && strings.HasPrefix(err.Error(), "config error: ")
        // want: os.ReadFile(p) == body
    }

    // TestLitellmSyncWorksOnValidationOnlyError (body of :721-743): a registry
    // that loads but fails validation (a model naming provider "ghost") is
    // still synced; the running model is routed and ghost/m is not.
    // Fixture: modelHome(t, <docs/contracts/registry.sample.toml> + the ghost
    // row, as validationOnlyApp :678-691 builds it); assert
    //   cfg, err := config.Load(); err == nil && cfg.Validate() != nil
    // instead of a.hostConfigErr()/hostLoadErrOf(a); then litellmEnv,
    // OPENROUTER_API_KEY/PINNED_CLOUD_API_KEY = "sk-test", stubProbeInventory,
    // execLitellm(t, "sync") == nil and the same config.yaml assertions.
    ```
    The sample path from `cmd/wt-model` is the same `filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml")`.
  - `TestLitellmSyncGatesOnLoadErrOnly` (:749-760) is **deleted, not moved**: it pinned which `*app` field the gate read by building a combination its own comment says cannot occur in production; the two tests above pin both halves of the rule through real files.
  - `cmd/wt-model/litellm_dup_test.go` (the git mv): `dupSyncApp` becomes
    ```go
    // dupSyncConfig loads dupSyncRegistry from a throwaway home through
    // config.Load, as the command does, and returns the config and the
    // registry's path. The load succeeds; validation fails on the id.
    func dupSyncConfig(t *testing.T) (*config.Config, string)
    // asserts: err == nil, cfg.Validate() != nil and contains `duplicate model id "local/qwen"`
    ```
    and every `hostCfgOf(a)` becomes the returned cfg; comments say `wt-model litellm sync`. Test names unchanged: `TestDesiredLocalModelsPairADuplicatedIDByProvider` (if PR 3a left it here), `TestLitellmSyncLeavesTheRouteOfADuplicatedIDAlone`, `TestSyncDownWarningIgnoresADuplicatedID` (if PR 3a left it here).
  - `TestLitellmLoadErrorNamesTheRepairThatWorks` moves to `cmd/wt-model/load_error_hint_test.go` with `root := rootCmd(fakeDeps)` in place of `rootCmdWith(testHooks())`, args `{"litellm", "sync"}`, and the file's existing `loadErrorCases()`, `brokenHome`, `c.check` (from PR 4a-ii); doc comment names `wt-model litellm sync`.
  - Ensure `cmd/wt-model/testmain_test.go` stubs the proxy readiness exactly as `cmd/wt-session/testmain_test.go:117-127` does (`litellm.StubProxyReadinessForTest` with a probe returning false and a wait returning `"proxy readiness wait not stubbed in this test: " + url`); add the block if `grep -n StubProxyReadinessForTest cmd/wt-model/testmain_test.go` finds none.
- [ ] **Step 2: Run them to see them fail.** `cd wt && go test ./cmd/wt-model -run 'TestLitellmSync|TestSync|TestStartHook|TestEnsureRoute|TestDesiredLocal|TestLitellmLoadError' -count=1` → compile error `undefined: runLitellmSync` (and, once that is stubbed, `unknown command "sync" for "wt-model litellm"`). `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestLitellmRemovedExposeCommandsFailAndPointToSync' -count=1` → panic `moved command litellm sync is still registered` while the root is built (Task 36's `addMovedStubs`); after Step 3 the #179 assertion is the one that still needs its text.
- [ ] **Step 3: Implement.**
  - `cmd/wt-model/litellm.go`: `func litellmCmd(h *host) *cobra.Command`; `syncC`: `Use: "sync"`, `Short: "Make LiteLLM routes match the registry's cloud models and the running (or pulled ollama) local models"` (wt's text, :635), `Args: cobra.NoArgs`, `SilenceUsage: true`, flags `--json` ("print one JSON document (docs/contracts/litellm-cli.sample.json)") and `--dry-run` (wt's text, :645), `RunE`: `cfg, loadErr := h.hostConfig(); if loadErr != nil { return config.ConfigError(loadErr) }; return runLitellmSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, syncJSON, syncDryRun)`. `Long`:
    ```
    Make LiteLLM's config.yaml routes match the registry: every configured cloud
    model, plus every running local model (an ollama model while it is pulled).
    It reads registry.toml (a load error refuses, exit 1, nothing written; a
    registry that only fails validation is still synced) and restarts the proxy
    only when config.yaml changed.

    Flags:
      --json      print one JSON document (docs/contracts/litellm-cli.sample.json)
      --dry-run   print the route plan and the probe warnings the real sync would
                  report; change nothing (does not report litellm_settings fixes)

    Exit codes:
      0  done
      1  any failure, including a model whose route could not be built
    ```
    `c.AddCommand(statusC, setC, syncC, listC, provC)`.
  - `cmd/wt-model/main.go:34`: `litellmCmd(h)`.
  - `cmd/wt-session/litellm.go`: the deletions under **Files**; `removedLitellmCmd`'s error: ``fmt.Errorf("`wt litellm %s` was removed (#179): configured cloud models and running (or pulled ollama) local models are routed automatically — run `wt-model litellm sync`", name)``.
  - wt-session's `probeInventory`: run `cd wt && grep -n 'probeInventory' cmd/wt-session/*.go | grep -v _test.go`. When the only hit is its declaration in `litellm.go` (expected: sync was its last reader — `syncRoutesAfterWrite` went in Task 45, `desiredRouteIDs` in Task 31, `runLitellmSync` in this task), delete the declaration, its doc comment, the TestMain assignment (`testmain_test.go:54`), `stubProbeInventory` (`testmain_test.go:134-141`) and every remaining `probeInventory =`/`stubProbeInventory(` line in `cmd/wt-session/*_test.go` (each is then a seam that guards nothing; `launch_remote_test.go:281`'s panic stub included — the session path cannot reach a probe it does not link). When another non-test reader remains, leave all of it.
  - Comments at `main.go:44-48` and `boundary_test.go:166`: "the host ones (`wt stop <provider>`, `--all`)".
- [ ] **Step 4: Run the tests, expect PASS.** `cd wt && go test ./cmd/wt-model -count=1`; `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestLitellm|TestHostCommandsStillLoadTheRegistry|TestBoundary|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestLaunchRemote' -count=1`; `go vet ./... && gofmt -l .`; `go test ./internal/depcheck -count=1`.
- [ ] **Step 5: Commit.** Files: `wt/cmd/wt-model/{litellm.go,litellm_sync_test.go,litellm_dup_test.go,load_error_hint_test.go,main.go,testmain_test.go}` (plus `litellm_test.go` if a helper was adjusted), `wt/cmd/wt-session/{litellm.go,litellm_test.go,moved.go,load_error_hint_test.go,main.go,boundary_test.go,testmain_test.go}` and any `cmd/wt-session/*` the `probeInventory` step touched (`loading_test.go` among them) — `feat(wt-model): move litellm sync to wt-model; wt litellm sync points there`

---

### Task 51: `wt-model litellm restart` (reads only the proxy pair); `wt litellm restart` becomes a pointer; `bin/update/litellm` runs wt-model

**Files:**
- Modify `wt/cmd/wt-model/litellm.go`: `restart` subcommand, `runLitellmRestart`.
- Modify `wt/cmd/wt-session/litellm.go`: delete `restartC` (:688-705), `runLitellmRestart` (:760-784), `restartC` in `c.AddCommand`; `litellmCmd(a *app)` → `litellmCmd()` (no subcommand reads `a` any more); `litellmCmd`'s `Short` → `"Show and set the LiteLLM proxy url and key"`; drop the `strings` and `internal/litellm` imports if unused.
- Modify `wt/cmd/wt-session/main.go:641`: `litellmCmd(a)` → `litellmCmd()`.
- Modify `wt/cmd/wt-session/moved.go`: one row.
- Modify `bin/update/litellm:24-31, 75-82, 149-152, 203-213`.
- Test: `wt/cmd/wt-model/litellm_test.go`, `wt/cmd/wt-session/litellm_test.go`.

**Interfaces:**
- Consumes: `config.ResolveProxy() (config.ProxyPair, error)` (`internal/config/proxyhome.go:105`; `ProxyPair{URL, APIKey string; Source ProxySource}`), `config.ConfigError`, `config.ShownProxyURL(raw string) string`, `litellm.RestartAndAwait(baseURL string) []string` (`internal/litellm/restart.go:458`), `litellm.StubProxyReadinessForTest`.
- Produces (cmd/wt-model): `func runLitellmRestart(out io.Writer, pair config.ProxyPair, pairErr error) error` — `pairErr != nil` → `config.ConfigError(pairErr)` and no restart; else `url := strings.TrimRight(pair.URL, "/")` (what `Config.LitellmBaseURL` did, `config.go:79`) and then today's body (:775-783) unchanged, messages byte for byte. The command's `RunE` is `pair, err := config.ResolveProxy(); return runLitellmRestart(cmd.OutOrStdout(), pair, err)`: no `hostConfig()`, no registry read. (cmd/wt-session) `func litellmCmd() *cobra.Command`.

- [ ] **Step 1: Write the failing tests.**
  - `cmd/wt-session/moved.go`: append `{Old: "litellm restart", New: "litellm restart"}`.
  - Move `TestLitellmRestartWaitsAndReports` (:151-203) and `TestLitellmRestartHidesAnAtURL` (:208-224) to `cmd/wt-model/litellm_test.go`; replace `cfg := litellmTestConfig(); cfg.SetLitellmForTest(config.LitellmState{URL: url})` + `runLitellmRestart(&out, cfg)` with `runLitellmRestart(&out, config.ProxyPair{URL: url}, nil)`; doc comments name `wt-model litellm restart` (and "`bin/update/litellm` runs it"). Assertion values unchanged (`"restarted the LiteLLM proxy at "+url+"\n"`, `"exit status 3"`, `"LiteLLM proxy not ready at "+url`, `config.ProxyURLHidden`).
  - New in `cmd/wt-model/litellm_test.go`:
    ```go
    // TestLitellmRestartReadsOnlyTheProxyPair pins what the restart reads: the
    // pair alone. An unresolvable pair refuses before anything restarts (a
    // restart that skipped its wait as "no url" would hide the broken pair);
    // no url restarts unwaited with today's line; a trailing slash is trimmed
    // as Config.LitellmBaseURL trimmed it.
    func TestLitellmRestartReadsOnlyTheProxyPair(t *testing.T)
    // case "unresolvable pair": WT_LITELLM_RESTART_CMD = "echo restart >> <tmp>/restarts";
    //   err := runLitellmRestart(&out, config.ProxyPair{}, errors.New("bad home"))
    //   want strings.HasPrefix(err.Error(), "config error: "), out empty, <tmp>/restarts absent
    // case "no url": readiness stub records steps; runLitellmRestart(&out, config.ProxyPair{}, nil) == nil,
    //   out == "restarted the LiteLLM proxy (no [litellm] url, so it was not waited for)\n", steps empty
    // case "trailing slash": URL "http://proxy.test:4000/"; steps == ["wait http://proxy.test:4000 after \"restart\\n\""],
    //   out == "restarted the LiteLLM proxy at http://proxy.test:4000\n"

    // TestLitellmRestartNeedsNoRegistry runs `wt-model litellm restart` through
    // the real tree over a registry.toml that does not parse: the restart reads
    // no registry, so bin/update/litellm can restart the proxy on a machine
    // whose registry is broken.
    func TestLitellmRestartNeedsNoRegistry(t *testing.T)
    // proxyEnv(t); write <home>/local-ai/registry.toml = "models = [\n";
    // WT_LITELLM_RESTART_CMD = "echo restart >> <tmp>/restarts"; readiness stub (no wait expected: no pair)
    // stdout, _, err := execLitellm(t, "restart"); want err == nil,
    //   stdout == "restarted the LiteLLM proxy (no [litellm] url, so it was not waited for)\n",
    //   os.ReadFile(<tmp>/restarts) == "restart\n"

    // TestWtModelLitellmHelpListsRestart pins the line bin/update/litellm
    // feature-detects (`wt-model litellm --help | grep -Eq
    // '^[[:space:]]+restart[[:space:]]'`): without it the updater falls back to
    // launchctl kickstart -k, which cuts off a request in flight.
    func TestWtModelLitellmHelpListsRestart(t *testing.T)
    // stdout, _, err := execLitellm(t, "--help"); err == nil;
    // regexp.MustCompile(`(?m)^\s+restart\s`).MatchString(stdout) == true;
    // the same regexp holds for sync, list, providers, status and set
    ```
  - New in `cmd/wt-session/litellm_test.go`:
    ```go
    // TestWtLitellmHelpListsOnlyTheProxyPairCommands pins how `wt litellm
    // --help` reads after the move: status and set, and no line for a moved
    // command (their pointer stubs are hidden). An old bin/update/litellm
    // grepped this help for restart; it must not find one and run a pointer.
    func TestWtLitellmHelpListsOnlyTheProxyPairCommands(t *testing.T)
    // out, code := runRoot(t, "litellm", "--help"); code == 0;
    // `(?m)^\s+status\s` and `(?m)^\s+set\s` match;
    // `(?m)^\s+(sync|list|providers|restart|expose|unexpose|on|off)\s` does not match
    ```
  - `cmd/wt-session/litellm_test.go`: `execLitellm(a *app, args ...string)` (:30-36) → `execLitellm(args ...string) error` over `litellmCmd()`; callers `TestLitellmRemovedExposeCommandsFailAndPointToSync` (:562, :571) and `TestLitellmOnOffAreRetired` (:2190 `litellmCmd(&app{})`) drop the app argument. `TestLitellmRoutingCommandsWorkOnValidationOnlyError` (:699-714) is rewritten without the `*app` fixture: `validationOnlyApp` becomes `validationOnlyHome(t)` (same registry: the sample plus the `ghost/m` row through `modelHome`, no `newApp`), the fixture check becomes `cfg, err := config.Load(); if err != nil || cfg.Validate() == nil { t.Fatalf(…) }` (no `a.hostConfigErr()`/`hostLoadErrOf(a)`), and `execLitellm(args...)` runs `set` and `status` as before with the same `resolveProxy` assertion; the doc comment says status/set read only the proxy pair, so a registry that fails validation cannot reach them. After this no wt-session test calls `hostConfigErr` (check `grep -n 'hostConfigErr' cmd/wt-session/*_test.go` → only `boundary_test.go`). `TestLitellmRemovedExposeCommandsFailAndPointToSync`'s help check (:578) becomes `!strings.Contains(help.String(), "status") || strings.Contains(help.String(), "expose")` with message "help must list status and hide expose/unexpose". `litellmTestConfig` is then unused in wt-session: delete it there.
- [ ] **Step 2: Run them to see them fail.** `cd wt && go test ./cmd/wt-model -run 'TestLitellmRestart|TestWtModelLitellmHelp' -count=1` → compile error `undefined: runLitellmRestart`. `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestWtLitellmHelpListsOnlyTheProxyPairCommands' -count=1` → panic `moved command litellm restart is still registered` while the root is built (Task 36's `addMovedStubs`).
- [ ] **Step 3: Implement.**
  - `cmd/wt-model/litellm.go`: `runLitellmRestart` as under **Interfaces**; `restartC`: `Use: "restart"`, `Short` = wt's (:690), `Args: cobra.NoArgs`, `SilenceUsage: true`, `Long`:
    ```
    Restart the LiteLLM proxy the way `wt-model litellm sync` does after a route
    change.

    With WT_LITELLM_RESTART_CMD set, that command is run as given. Otherwise
    wt-model sends the proxy's process one SIGTERM, waits up to 3 minutes for a
    request in flight to finish, and has launchd start it again; `launchctl
    kickstart -k` would kill that request after 5 seconds. It then waits up to
    30 seconds for the proxy at the pair's url to answer (the wt-model home,
    else the legacy [litellm] table; with no url it is not waited for). It reads
    no registry. bin/update/litellm runs it after an upgrade.

    Exit codes:
      0  restarted
      1  the pair could not be read, the restart failed, or the proxy did not
         answer again
    ```
    `c.AddCommand(statusC, setC, syncC, listC, provC, restartC)`.
  - `cmd/wt-session/litellm.go`, `main.go`: as under **Files**.
  - `bin/update/litellm`:
    - :24-31 header: "Without one it is `wt-model litellm restart`, the restart `wt-model litellm sync` makes: …" (rest unchanged), and "`launchctl kickstart -k` is only the fallback when no wt-model on PATH has that command".
    - :75-82 rename `wt_restarts` → `wt_model_restarts`; comment "whether the wt-model on PATH has `wt-model litellm restart` (it moved there from wt in phase 4 stage 2; `restart` under `wt litellm` is now a hidden pointer that exits 1). Asked of the subcommand list, not of `restart --help`: cobra answers an unknown subcommand's --help with the parent's help and exit 0. update-tools updates litellm before wt, so the wt-model here can predate the command."; body `command -v wt-model > /dev/null 2>&1 && wt-model litellm --help 2> /dev/null | grep -Eq '^[[:space:]]+restart[[:space:]]'`.
    - :149-152 `elif [ -f "$PLIST" ] && wt_model_restarts; then update_note "would run: wt-model litellm restart"`; fallback note `"would run: launchctl kickstart -k gui/$(id -u)/local.litellm.proxy (no wt-model with 'litellm restart' on PATH; a request in flight is cut off)"`.
    - :203-208 `elif [ -f "$PLIST" ] && wt_model_restarts; then` … comment "wt-model prints the drain notice itself …"; `run wt-model litellm restart || update_fail "wt-model litellm restart failed"`; `restarted="the proxy was restarted by wt-model litellm restart and re-read config.yaml"`.
    - :210 `update_note "no wt-model with 'litellm restart' on PATH (update wt: make install builds wt-model); falling back to launchctl kickstart -k, which cuts off a request in flight"`.
- [ ] **Step 4: Run the tests, expect PASS.**
  - `cd wt && go test ./cmd/wt-model -count=1`; `go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestLitellm|TestWtLitellmHelp|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage|TestOneErrorLine' -count=1`; `go vet ./... && gofmt -l .`.
  - From the repo root: `make lint-shell`.
  - The updater's dry run against stubs (scratch only, nothing committed): `D=$(mktemp -d)`; `mkdir -p "$D/bin" "$D/home/.local/share/uv/tools/litellm/lib/python3.11/site-packages/litellm/proxy"`; `touch "$D/home/.local/share/uv/tools/litellm/lib/python3.11/site-packages/litellm/proxy/schema.prisma" "$D/plist"`; `printf 'extra-proxy\npython = "3.11"\n' > "$D/home/.local/share/uv/tools/litellm/uv-receipt.toml"`; stubs `$D/bin/uv` (`#!/bin/bash` + `exit 0`), `$D/bin/litellm` (prints `LiteLLM: Current Version = 1.0.0`), `$D/bin/wt-model` (prints `Available Commands:` then `  restart     Restart` for `litellm --help`); run `env -i PATH="$D/bin:/usr/bin:/bin" HOME="$D/home" WT_LITELLM_PLIST="$D/plist" bin/update/litellm --dry-run` → output contains `would run: wt-model litellm restart`; remove `$D/bin/wt-model` and rerun → contains `no wt-model with 'litellm restart' on PATH`.
- [ ] **Step 5: Commit.** `git add bin/update/litellm wt/cmd/wt-model/litellm.go wt/cmd/wt-model/litellm_test.go wt/cmd/wt-session/litellm.go wt/cmd/wt-session/litellm_test.go wt/cmd/wt-session/main.go wt/cmd/wt-session/moved.go` — `feat(wt-model): move litellm restart to wt-model; bin/update/litellm runs wt-model litellm restart`

---

### Task 52: Go hint strings and comments name `wt-model litellm …`

**Files:**
- Modify `wt/internal/modelserver/deps.go:169,171` (the gateway route wait's two timeout errors).
- Modify `wt/internal/modelsetup/plan.go:255-263` (`restartNote` text and comment) and goldens `wt/internal/modelsetup/testdata/plan_litellm_rotate.golden:15`, `plan_remote.golden:9` (and any `modelmode/testdata/*.golden` holding the same note after PR 1c).
- Modify comments that quote the moved commands (no behaviour): `wt/internal/config/registry_doc.go:164`, `wt/internal/lifecycle/lifecycle.go:669`, `routes.go:134,177,286,611,657`, `routes_test.go:672,840`, `wrappers_test.go:339`, `wt/internal/litellm/configfile_test.go:769`, `policy_table_test.go:35`, `restart.go:389,443,455`, `restart_wait_test.go:64,189`, `service.go:132,489,994`, `service_dup_test.go:12`, `service_test.go:764,917`, `wt/internal/localmodels/inventory.go:76`, `inventory_test.go:220,396,699`, `wt/internal/modelsetup/finalreview_test.go:199`, `wt/internal/provider/provider.go:45`, and any `cmd/wt-model`/`cmd/wt-session` comment the grep in Step 4 still finds. Rule: `wt litellm sync|list|providers|restart` → `wt-model litellm sync|list|providers|restart`; nothing else in the line changes.
- Test: `wt/internal/modelserver/autostop_test.go` (`TestRoutedTimesOut`, :218-237).

**Interfaces:** none new.

- [ ] **Step 1: Write the failing test.** In `TestRoutedTimesOut` after the first wait: `if !strings.Contains(err.Error(), "'wt-model litellm list'") { t.Errorf("wait = %v, want the hint naming 'wt-model litellm list'", err) }` (the stub answers `{"data":[]}` with no transport error, so the second branch, :171, is the one returned). Doc comment: "…with an error naming the id and the host command that shows the routes…".
- [ ] **Step 2: Run it to see it fail.** `cd wt && go test ./internal/modelserver -run TestRoutedTimesOut -count=1` → FAIL: the error names `'wt litellm list'`.
- [ ] **Step 3: Implement.** `deps.go:169` → `…check that the LiteLLM proxy answers, or run 'wt-model litellm restart'`; `:171` → `…check the LiteLLM routes with 'wt-model litellm list'`. `plan.go:261` and its comment `:257` → ``unlike `wt-model litellm restart`, nothing waits for them to finish``; regenerate the two goldens by editing the one line each (the golden tests compare bytes; do not run an update flag). Then the comment rule over the files listed.
- [ ] **Step 4: Run the tests, expect PASS.** `cd wt && go test ./internal/modelserver ./internal/modelsetup ./internal/lifecycle ./internal/litellm ./internal/localmodels ./internal/config ./internal/provider -count=1` (add `./internal/modelmode` if it has goldens); `go vet ./... && gofmt -l .`; `git grep -nE "wt litellm (sync|list|providers|restart)" -- 'wt/*.go' 'wt/**/testdata/*'` prints nothing.
- [ ] **Step 5: Commit.** All files above — `fix(wt): name wt-model litellm sync|list|restart in hints, notes and comments`

---

### Task 53: Docs, skills, CLAUDE.md files, CHANGELOG and the fixture comment

**Files (each line naming `wt litellm sync|list|providers|restart` is rewritten to `wt-model litellm …`; `wt litellm status|set` lines stay):**
- `CLAUDE.md:67` (`wt litellm list` → `wt-model litellm list`), `:68` (`wt litellm list` likewise), `:48` (the `wt/` bullet: "**all LiteLLM management** (`wt-model litellm sync|list|providers|restart`, and `wt litellm status|set` with their `wt-model` twins; …)").
- `wt/CLAUDE.md:118` (`wt-model litellm list`); the section "## LiteLLM routes (`wt litellm`)" becomes "## LiteLLM routes (`wt-model litellm`)": "`cmd/wt-model/litellm.go` + `internal/litellm` … Subcommands `sync|list|providers|restart|status|set`; `wt litellm` keeps `status|set` (twins) and hidden pointer stubs for the four moved subcommands (phase 4 stage 2) besides the retired `on|off`; `sync` is the only CLI route write, `restart` is what `bin/update/litellm` runs (it feature-detects `wt-model litellm restart`)"; the bullet naming "`cmd/wt-session/litellm_test.go`" fixtures → `cmd/wt-model/litellm_test.go`; the wt-model section's sentence "The same binary's `litellm set|status`" → "`litellm sync|list|providers|restart|set|status`"; the Verification block line `wt litellm` stays only for `status|set`.
- `wt/docs/internals/litellm-routes.md:17,25-27,40` (:26 "`wt-model litellm restart` (`cmd/wt-model`'s `runLitellmRestart`) is `RestartAndAwait` on the proxy pair's url (`config.ResolveProxy`, no registry read) …"; :27 "`bin/update/litellm` when no wt-model on PATH has `litellm restart`").
- `wt/docs/wt-agents/litellm-troubleshooting.md:280,285,401`; `wt/docs/wt-agents/README.md:25-26,52,56,85,94,106,111` (:94 "with no wt-model on PATH that has `litellm restart`").
- `docs/guides/04-litellm-config.md` (every hit; the command table rows :338-343 become `wt-model litellm …`; :345 "they remain only as hidden stubs that exit 1 and point to `wt-model litellm sync`" and one added sentence: "`sync`, `list`, `providers` and `restart` moved from `wt litellm` to `wt-model litellm` in phase 4 stage 2; under `wt` they print a pointer and exit 1.").
- `docs/guides/08-maintenance-and-troubleshooting.md` (every hit; :367 and :399 describe `bin/update/litellm` running `wt-model litellm restart`).
- The other current docs the design's same-change-set rule covers: `README.md:26`, `benchmarks/README.md:67,75`, `benchmarks/ornith-1.5-benchmark.md:163`, `benchmarks/qwen3.8-benchmark.md:307,314,322`, `benchmarks/suites/eval-four-way.toml:17`, `docs/guides/00-config-map.md:141,142,341`, `01-initial-setup.md:74,90,396,435,491`, `02-providers-and-models.md` (every hit), `03-model-families.md:31`, `06-wt-agents-and-models.md:131-144`, `10-mlx-lm-quantization.md:65,100`, `14-litellm-install-and-configuration.md` (every hit), `docs/reference/glm-5.3-flash-openrouter.md`, `litellm-admin-ui-setup.md`, `provider-artifacts.md` (every hit), `wt/docs/internals/config-and-registry.md`, `local-models.md:42`, `testing.md:23,86` (:23 names cmd/wt-model's `probeInventory` seam for sync), `wt/docs/wt-agents/slot-profiles.md:732`, `wt/docs/wt-cloud-sync.md:432,437,667`, `wt/docs/wt-config.md:67`, `wt/docs/wt-model-server.md:136`, `wt/docs/wt-model.md:354,467`, `wt/docs/wt-session.md:239` ("`wt litellm status|set`"; the moved four are listed under `wt-model`), `wt/docs/wt-start-stop.md:122,124,507,523`.
- Skills: `.claude/skills/wt-adding-a-benchmark-backend/SKILL.md:127-128` (`wt-model litellm sync`, `wt-model litellm list`), `.claude/skills/wt-mlx-lm-quantization/SKILL.md:10`, `wt/.claude/skills/wt-cloud-sync/SKILL.md:211`.
- `docs/contracts/litellm-cli.sample.json:2` `_comment`: "Fixture for the shapes `wt-model litellm ... --json` (and its `wt litellm status --json` twin) prints. Read by wt/cmd/wt-model/litellm_test.go and wt/cmd/wt-session/litellm_test.go." (no test reads `_comment`; the two readers stay: wt-session's `TestLitellmStatusHasNoEnabledKey`, wt-model's contract tests).
- `wt/CHANGELOG.md`, under `## Unreleased` → `### Changed`, first bullet:
  ```
  - **Breaking:** `wt litellm sync`, `list`, `providers` and `restart` moved to
    `wt-model litellm sync|list|providers|restart` (same flags, output, JSON and
    exit codes). The old names print `wt: litellm <name> moved to wt-model — run
    'wt-model litellm <name>'; nothing was changed` and exit 1; `wt litellm
    status|set` stay in both binaries. `wt-model litellm restart` reads only the
    proxy pair: a config.toml or registry that does not load no longer affects
    it (before, a config that failed to load made it restart without waiting).
    `bin/update/litellm` feature-detects and runs
    `wt-model litellm restart`.
  ```
- Not changed: `docs/superpowers/**`, `docs/archive/**`, earlier `wt/CHANGELOG.md` entries (history).

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** `cd <repo root> && git grep -nE "wt litellm (sync|list|providers|restart)" -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!benchmarks/results' ':!wt/CHANGELOG.md' ':!issues.md' ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go'` lists the lines above (non-empty output is the failure).
- [ ] **Step 2: Run it.** The command above prints the hits listed under **Files**.
- [ ] **Step 3: Implement.** The rewrites listed; prose that described the old binary's behaviour (e.g. "a wt too old to have `restart` under `wt litellm`") is reworded to the wt-model case as given above, never left naming the old command. Guides stay state-independent (no live counts); home paths stay `/Users/user_name`.
- [ ] **Step 4: Run the checks, expect PASS.** The `git grep` prints nothing; `make check-links`; `make lint-skills`; `cd wt && go test ./cmd/wt-model -run 'Contract|Fixture' -count=1 && go test ./cmd/wt-session -run 'TestLitellmStatusHasNoEnabledKey' -count=1`.
- [ ] **Step 5: Commit.** All files above — `docs: wt-model litellm sync|list|providers|restart in guides, skills, CLAUDE.md and the changelog`

---

**PR gate (after Task 53):** the Exit commands above; the PR body lists the four pointer lines, the restart's no-registry read (and its one behaviour change: a config.toml or registry that fails to load no longer makes it restart unwaited; an unreadable proxy pair already refused today, `litellm.go:767-772`), the `bin/update/litellm` detection change with the stub dry-run evidence, and `Refs` to the phase 4 stage 2 issue (never close/fix/resolve + #N).

---

## PR 4d: the serve runtime record, and provider stop / stop --all move to wt-model

The spine (§13) ships this stage as two PRs, **4d-i** (Tasks 54 to 57) and **4d-ii** (Tasks 58 to 63). Each has its own entry and exit block. Line numbers are on `origin/main` f60ad48a. PRs 3b to 4c move or add code in the same files before this stage runs, so a cited line can drift: find it by the quoted name.

**4d-i**
- **Entry:** PR 4c merged (`wt-model litellm sync|list|providers|restart` exist; `cmd/wt-model` has `host.go` (3b), `cli` (4a-i), the moved model/served/warm/cloud-sync commands and their `testmain_test.go` stubs).
- **Exit gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1`, then `cd wt && go test ./internal/depcheck ./internal/removal ./internal/servestate ./internal/config ./cmd/wt-model -count=1`, then from the repo root `make check-links`.

**4d-ii**
- **Entry:** 4d-i merged (`internal/servestate` exists, serve writes `serve.json`).
- **Exit gate:** the 4d-i gate, plus `cd wt && go test ./internal/hoststop ./internal/lifecycle -count=1`, from the repo root `make lint-shell` and `make check-links`, and the Task 62 Step 4 grep printing nothing.

Decisions this section pins (each within the spine):
- Record file: `<wt-model home>/serve.json`, JSON `{"version":1,"pid":…,"listen":"…","token_file":"…","started":"…"}`. `const RecordVersion = 1`. `Write` always stores `RecordVersion`.
- Serve writes the record **before** it prints the announce line, both inside the announce callback, so anything that sees the line can already read the record. A failed write is one stderr note, and serve goes on.
- `Sessions` dials `127.0.0.1:<port>` when the recorded host is unspecified (`0.0.0.0`, `::`). `--listen 0.0.0.0:8787` is a documented form, and its bound address reads `[::]:8787`.
- `wt-model stop` output lines use the `wt-model: ` prefix (was `wt: `). The re-run hint names `wt-model stop <provider>`. Question texts stay byte for byte as today.
- `wt-model stop` never prints usage (`SilenceUsage: true`, as `wt-model`'s root already does). In `wt-session` an argument mistake showed usage. Here the error line names the valid providers.

---

### Task 54: `config.ModelHome`

**Files:**
- Modify: `wt/internal/config/proxyhome.go:78-87` (`ProxyHomePath`)
- Test: `wt/internal/config/proxyhome_test.go` (next to `TestProxyHomeFollowsTheConfigHome`, :142)

**Interfaces:**
- Consumes: `baseConfigHome() string` (`config/config.go:739`).
- Produces: `func ModelHome() string`, which returns `<baseConfigHome>/wt-model`, or `""` when there is no config home. `ProxyHomePath()` becomes `filepath.Join(ModelHome(), "config.toml")`, still `""` when `ModelHome()` is `""`.

- [ ] Step 1: Write the failing test `TestModelHomeIsTheProxyHomesDirectory` (top-level comment: servestate and the proxy pair must name one directory, so the record never lands beside a different home). Assertions:
  ```go
  dir := t.TempDir()
  t.Setenv("XDG_CONFIG_HOME", dir)
  if got, want := ModelHome(), filepath.Join(dir, "wt-model"); got != want { t.Errorf("ModelHome() = %q, want %q", got, want) }
  if got, want := ProxyHomePath(), filepath.Join(ModelHome(), "config.toml"); got != want { t.Errorf("ProxyHomePath() = %q, want %q", got, want) }
  t.Setenv("XDG_CONFIG_HOME", ""); t.Setenv("HOME", "")
  if got := ModelHome(); got != "" { t.Errorf("ModelHome() with no home = %q, want \"\"", got) }
  if got := ProxyHomePath(); got != "" { t.Errorf("ProxyHomePath() with no home = %q, want \"\"", got) }
  ```
- [ ] Step 2: `cd wt && go test ./internal/config -run TestModelHomeIsTheProxyHomesDirectory -count=1`. Expect a build failure: `undefined: ModelHome`.
- [ ] Step 3: Add `func ModelHome() string` in `proxyhome.go` directly above `ProxyHomePath`, with a doc comment ("the wt-model home directory: the proxy pair's config.toml and key, and serve's runtime record"). `ProxyHomePath` calls it. No other call site changes.
- [ ] Step 4: `cd wt && go test ./internal/config -count=1`. Expect PASS.
- [ ] Step 5: Commit `feat(config): ModelHome names the wt-model home directory`. Files: `wt/internal/config/proxyhome.go`, `wt/internal/config/proxyhome_test.go`.

### Task 55: `internal/servestate`: Record, Store, Write/Read/Remove

**Files:**
- Create: `wt/internal/servestate/record.go`
- Create: `wt/internal/servestate/record_test.go`
- Create: `wt/internal/servestate/testmain_test.go`
- Modify: `wt/internal/removal/deletionscan_test.go:33-50` (`fileOpAllowed`)
- Modify: `wt/internal/depcheck/depcheck_test.go:16-21` (`adminBanned`) and its comment :12-15

**Interfaces:**
- Consumes: `config.ModelHome()` (Task 54), `config.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` (`config/config.go:1336`), `modelmode.GuardUnderTest(paths ...string) error` (spine §3.4, PR 1b).
- Produces (spine §11, exact):
  ```go
  const RecordVersion = 1
  type Record struct {
      Version   int       `json:"version"`
      PID       int       `json:"pid"`
      Listen    string    `json:"listen"`     // the bound address serve announced
      TokenFile string    `json:"token_file"` // absolute path; never the token
      Started   time.Time `json:"started"`
  }
  type Store struct{ Path string }
  func Default() Store                        // Store{Path: filepath.Join(config.ModelHome(), "serve.json")}; Path "" when ModelHome() is ""
  func (s Store) Write(r Record) error
  func (s Store) Read() (Record, bool, error)
  func (s Store) Remove(pid int) error
  ```

- [ ] Step 1: Write the failing tests in `record_test.go`. Each test gets a top-level comment saying what it checks and why. Every store is `Store{Path: filepath.Join(t.TempDir(), "home", "serve.json")}` unless the test says otherwise. `rec := Record{PID: 4242, Listen: "127.0.0.1:8787", TokenFile: tokPath, Started: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}`, where `tokPath` is a 0600 temp file holding `tok-secret-xyz`.
  - `TestWriteThenReadRoundTrips`: `Write(rec)` is nil. `Read()` returns `ok == true` and nil error, `got.Version == RecordVersion`, `got.PID == 4242`, `got.Listen == "127.0.0.1:8787"`, `got.TokenFile == tokPath`, and `got.Started.Equal(rec.Started)`.
  - `TestWriteStoresVersionOneWhateverItIsGiven`: `Write(Record{Version: 7, PID: 1})`, then `Read()` gives `Version == 1`.
  - `TestWriteMakesA0600FileInA0700Directory`: after `Write`, `os.Stat(s.Path).Mode().Perm() == 0o600` and `os.Stat(filepath.Dir(s.Path)).Mode().Perm() == 0o700` (the `home` dir did not exist before).
  - `TestRecordNeverHoldsTheToken`: the raw file bytes do not contain `tok-secret-xyz`. Unmarshal them into `map[string]any`; the sorted keys equal `[]string{"listen", "pid", "started", "token_file", "version"}`.
  - `TestReadAbsentIsNotAnError`: on a fresh store, `Read()` returns `(Record{}, false, nil)`.
  - `TestReadRefusesAMalformedRecord`: write `{"pid":` to `s.Path` (0600). `Read()` returns `ok == false`, `err != nil`, and `strings.Contains(err.Error(), s.Path)`.
  - `TestReadRefusesAnUnknownVersion`: write `{"version":2,"pid":1}`. `Read()` gives an error whose text is exactly `s.Path + ": record version 2, want 1"`.
  - `TestRemoveOnlyRemovesItsOwnPid`: `Write(rec)`. `Remove(4243)` is nil, and the record is still readable with PID 4242. `Remove(4242)` is nil, and `Read()` now gives `ok == false`.
  - `TestRemoveOfAnAbsentRecordIsNil`: `Remove(1)` on a fresh store is nil.
  - `TestRemoveLeavesAMalformedRecord`: write `{"pid":`. `Remove(1)` returns non-nil, and the file still exists.
  - `TestEmptyStoreIsANoOp`: with `Store{}`, `Write(rec)` is nil, `Read()` is `(Record{}, false, nil)` and `Remove(1)` is nil. Nothing is written: compare a `t.TempDir()` listing before and after (cwd-independent; no file named `serve.json` appears in the cwd).
  - `TestDefaultIsInTheModelHome`: `t.Setenv("XDG_CONFIG_HOME", d)`. Then `Default().Path == filepath.Join(d, "wt-model", "serve.json")`. With XDG and HOME both `""`, `Default().Path == ""`.
  - `TestWriteRefusesAPathOutsideTheTempDirUnderTest`: `Store{Path: "/wt-servestate-guard/serve.json"}.Write(rec)` returns non-nil, its text contains `/wt-servestate-guard/serve.json`, and `os.Stat("/wt-servestate-guard")` reports not-exist.
  - `testmain_test.go`: `TestMain` calls `config.IsolateConfigHomeForTest()` and removes the dir after `m.Run()` (copy of `cmd/wt-model/testmain_test.go:16-24`, config line only). Its comment says no servestate test may reach the developer's wt-model home.
- [ ] Step 2: `cd wt && go test ./internal/servestate -count=1`. Expect a build failure: `undefined: Store` (the package has only test files).
- [ ] Step 3: Implement `record.go` (package doc: "the runtime record wt-model serve writes while it runs, so a host command can find the live server; it never holds the token").
  - `Write`: when `s.Path == ""`, return nil. Otherwise `modelmode.GuardUnderTest(s.Path)`, then `os.MkdirAll(filepath.Dir(s.Path), 0o700)` (an existing dir keeps its mode), set `r.Version = RecordVersion`, `json.MarshalIndent(r, "", "  ")` plus a trailing `"\n"`, and `config.WriteFileAtomic(s.Path, data, 0o600)`.
  - `Read`: when the path is `""` or `os.ReadFile` reports not-exist, return `(Record{}, false, nil)`. A decode error returns `fmt.Errorf("%s: %w", s.Path, err)`. A version other than `RecordVersion` returns `fmt.Errorf("%s: record version %d, want %d", s.Path, v, RecordVersion)`.
  - `Remove`: `Read`; return its error; `!ok` or `r.PID != pid` returns nil. Otherwise `os.Remove(s.Path)`, and not-exist reads as nil. This is the package's one `os.Remove`.
- [ ] Step 4: Add to `fileOpAllowed` (alphabetical, after `internal/scratchdir/scratchdir.go`):
  `"internal/servestate/record.go": {1, "Store.Remove: removes wt-model serve's own runtime record (serve.json in the wt-model home) on clean shutdown, only when it names this pid"},`.
  Add `"internal/servestate"` to `adminBanned` (after `"internal/modelsetup"`) and name it in the comment's list ("…, and the model server's own packages (… servestate)").
- [ ] Step 5: `cd wt && go test ./internal/servestate ./internal/removal ./internal/depcheck -count=1 && go vet ./internal/servestate && gofmt -l internal/servestate`. Expect PASS and no gofmt output.
- [ ] Step 6: Commit `feat(servestate): the wt-model serve runtime record (pid, listen, token file path)`. Files: `wt/internal/servestate/record.go`, `record_test.go`, `testmain_test.go`, `wt/internal/removal/deletionscan_test.go`, `wt/internal/depcheck/depcheck_test.go`.

### Task 56: `servestate.Sessions`

**Files:**
- Create: `wt/internal/servestate/sessions.go`
- Create: `wt/internal/servestate/sessions_test.go`

**Interfaces:**
- Consumes: `tokenfile.Load(path string) (string, error)` (`tokenfile/tokenfile.go:62`); `modelping.Endpoint{URL, Token}` and `modelping.GetJSON(ctx context.Context, ep Endpoint, path string, out any) error` (spine §2, PR 1a); `modelapi.Catalog{Rows []Row}`, `Row.ID`, `Row.Sessions` (`modelapi/modelapi.go:56,88,99`).
- Produces: `func Sessions(ctx context.Context, r Record) (map[string]int, error)` (spine §11). It returns the model ids whose `Row.Sessions > 0`, mapped to that count. The map is non-nil and empty when no row has a session. The unexported `func dialURL(listen string) string` returns `"http://127.0.0.1:" + port` when the host of `listen` is unspecified (`net.ParseIP(host).IsUnspecified()`), else `"http://" + listen`.

- [ ] Step 1: Write the failing tests. `sessions_test.go` carries a test-only fake: `fakeServer(t, token string, rows ...modelapi.Row) (srv *httptest.Server, catalogHits *atomic.Int32)`.
  - The fake answers `GET /control/v1/ping` with `modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"}` and `GET /control/v1/catalog` with `modelapi.Catalog{Schema: modelapi.SchemaVersion, Rows: rows}`.
  - It answers 401 with a `modelapi.APIError` body unless `Authorization == "Bearer "+token`, and counts catalog hits.
  - `rec(t, srv)` builds a `Record` with `Listen = srv.Listener.Addr().String()` and `TokenFile` = a 0600 temp file holding the token.

  Tests (each with a top-level comment):
  - `TestSessionsCountsFromTheCatalog`: rows are `omlx/busy` with Sessions 2 and `omlx/idle` with Sessions 0. `Sessions` returns `map[string]int{"omlx/busy": 2}` and nil error.
  - `TestSessionsWithNoSessionsIsAnEmptyMap`: rows `omlx/idle` with Sessions 0. The result is non-nil, `len == 0`, error nil.
  - `TestSessionsDialsLoopbackForAnUnspecifiedHost`: rewrite `Listen` to `"0.0.0.0:" + port` and then to `"[::]:" + port`. Both reach the fake (`catalogHits.Load() == 2`). Also a table test on `dialURL`: `"0.0.0.0:8787"` and `"[::]:8787"` give `"http://127.0.0.1:8787"`; `"127.0.0.1:8787"` and `"[::1]:8787"` are kept.
  - `TestSessionsErrorNeverCarriesTheToken`: the fake wants `right-token` and the token file holds `tok-secret-xyz`. The error is non-nil, its text contains neither `tok-secret-xyz` nor `right-token`, and `errors.As(err, &*modelping.UnauthorizedError)` is true.
  - `TestSessionsReportsAnUnreadableTokenFile`: `TokenFile` names a missing file. The error is non-nil and `errors.As(err, &*tokenfile.Error)` is true. The fake saw no request (ping hits 0).
  - `TestSessionsReportsAStaleRecord`: start the fake, read its address, `srv.Close()`. The error is non-nil and `errors.As(err, &*modelping.UnreachableError)` is true.
  - `TestSessionsGivesUpOnAServerThatNeverAnswers` (comment: a record whose address accepts and never answers — a hung server, or an unrelated process on the port — must not hang a host stop; the caller's context bounds it, Review Focus #5): a raw `net.Listen("tcp", "127.0.0.1:0")` whose accept loop holds each connection open without writing (closed in `t.Cleanup`); `ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)`; `Sessions(ctx, rec)` returns a non-nil error within 3 s of the call (measure with `time.Since`), and its text does not contain the token. (Task 58's host reader passes such a bounded context; if `modelping.GetJSON` does not honour `ctx` for the whole exchange, this test fails and the fix belongs in `modelping`.)
- [ ] Step 2: `cd wt && go test ./internal/servestate -run 'TestSessions' -count=1`. Expect `undefined: Sessions`.
- [ ] Step 3: Implement `sessions.go`: `tokenfile.Load(r.TokenFile)` (return its error as is), then `modelping.GetJSON(ctx, modelping.Endpoint{URL: dialURL(r.Listen), Token: tok}, "/control/v1/catalog", &cat)`, then the `Sessions > 0` filter. Doc comment: "a stale record is the caller's to tolerate: it is an unreachable server, an error".
- [ ] Step 4: `cd wt && go test ./internal/servestate ./internal/depcheck -count=1`. Expect PASS. `servestate`'s closure is `config, modelapi, modelmode, modelping, tokenfile, urlrule, provider, tomlw`, which is off `sessionBanned` by construction.
- [ ] Step 5: Commit `feat(servestate): read a running server's client sessions through its record`. Files: `wt/internal/servestate/sessions.go`, `sessions_test.go`.

### Task 57: serve writes the record and removes it on clean shutdown

**Files:**
- Modify: `wt/cmd/wt-model/main.go:23-36` (`rootCmd`), `:44` (`main`)
- Modify: `wt/cmd/wt-model/serve.go:61-129` (`serveCmd`)
- Modify: `wt/cmd/wt-model/serve_test.go:104-133` (`serveUntilListening`), `:160-170` (`run`), `:293` (`TestServeAnswersPingOnEphemeralPort`); `wt/cmd/wt-model/litellm_test.go:33`; every other `rootCmd(` call under `wt/cmd/wt-model/*_test.go` (list them with `grep -rn 'rootCmd(' wt/cmd/wt-model`)
- Create: `wt/cmd/wt-model/serve_record_test.go`
- Docs: `wt/docs/wt-model-server.md` ("Startup and shutdown", :330), `wt/CLAUDE.md` ("wt-model" section, :309), `wt/docs/internals/packages.md` (package table, a new `internal/servestate/` row after `internal/sessionreg/` :78; the ratchet sentence :91 adds `servestate`), `wt/CLAUDE.md` "Import graph ratchet" paragraph :307 (adds `servestate` to the admin list), `wt/CHANGELOG.md` (Unreleased → Added)

**Interfaces:**
- Consumes: `servestate.Store`, `servestate.Record`, `servestate.Default()`, `servestate.Sessions` (Task 55, 56).
- Produces (spine §11): `type runtime struct{ Record servestate.Store }`; `func rootCmd(mk depsFactory, rt runtime) *cobra.Command`; `func serveCmd(mk depsFactory, rt runtime) *cobra.Command`. In tests, `func testRuntime(t *testing.T) runtime` returns `runtime{Record: servestate.Store{Path: filepath.Join(t.TempDir(), "serve.json")}}` (in `serve_record_test.go`), and `func serveUntilListeningWith(t *testing.T, rt runtime, tokenPath string) (readStderr, readStdout func() string, stop func() error)`. `serveUntilListening(t)` becomes `serveUntilListeningWith(t, testRuntime(t), writeToken(t, 0o600))`.

- [ ] Step 1: Write the failing tests in `serve_record_test.go`. Each calls `seedConfig(t, true)` first.
  - `TestServeWritesTheRuntimeRecord` (comment: a host command finds the live server only through this record, and must never find the token in it). `rt := testRuntime(t)`, `tok := writeToken(t, 0o600)`, `readErr, _, stop := serveUntilListeningWith(t, rt, tok)`; parse `addr` from `readErr()` with the regex of `TestServeAnswersPingOnEphemeralPort`. Then:
    - `rec, ok, err := rt.Record.Read()` gives `ok && err == nil`, `rec.PID == os.Getpid()`, `rec.Listen == addr`, `rec.TokenFile == tok`, `rec.Version == 1`, and a non-zero `rec.Started`.
    - `os.Stat(rt.Record.Path).Mode().Perm() == 0o600`. The raw bytes do not contain `tok-abc`.
    - `servestate.Sessions(t.Context(), rec)` returns a non-nil map, `len == 0`, and nil error.
    - `stop()` is nil, and `rt.Record.Read()` then gives `ok == false` and nil error.
  - `TestServeKeepsARecordAnotherServerWrote` (comment: a second serve overwrote the record, so the first one's clean exit must not delete it). Serve as above. After the listening line, `rt.Record.Write(servestate.Record{PID: os.Getpid() + 1, Listen: "127.0.0.1:1", TokenFile: tok})`. `stop()` is nil, and `Read()` gives `ok` with `PID == os.Getpid()+1`.
  - `TestServeThatCannotListenLeavesTheRecordAlone`: `ln, _ := net.Listen("tcp", "127.0.0.1:0")`, deferred close. Seed `rt.Record` with `PID: os.Getpid() + 1`. Run `serve --listen <ln.Addr()> --token-file <tok>` through `rootCmd(fakeDeps, rt)`. The error contains `cannot listen on`. `Read()` gives `ok` with `PID == os.Getpid()+1`.
  - `TestServeKeepsServingWhenTheRecordCannotBeWritten`: `blocker := filepath.Join(t.TempDir(), "file")` is written as a regular file, and `rt := runtime{Record: servestate.Store{Path: filepath.Join(blocker, "serve.json")}}`. Serve until listening; stderr contains `wt-model: note: could not write the runtime record (`. An authenticated ping to the announced address answers 200 (copy the request from `TestServeAnswersPingOnEphemeralPort`). `stop()` is nil.
  - `TestMainKeepsTheRecordOffTheRealHome` (comment: the TestMain isolation is what keeps `servestate.Default()` (the store `main` wires) inside a scratch home in this binary). `p := servestate.Default().Path`. `strings.HasPrefix(p, os.Getenv("XDG_CONFIG_HOME")+string(filepath.Separator))`. `p` is under `os.TempDir()` (compare `filepath.EvalSymlinks` of both). When `os.UserHomeDir()` works, `p` does not start with `filepath.Join(home, ".config")`.
- [ ] Step 2: `cd wt && go test ./cmd/wt-model -run 'TestServe|TestMainKeeps' -count=1`. Expect build failures: `undefined: runtime`, and the wrong argument count to `rootCmd`.
- [ ] Step 3: Implement.
  - `main.go`: add `type runtime struct{ Record servestate.Store }` with a comment ("per-call seams of the commands: tests pass a scratch store"). `rootCmd(mk depsFactory, rt runtime)` passes `rt` to `serveCmd`. `main` calls `rootCmd(modelserver.DefaultDeps, runtime{Record: servestate.Default()})`.
  - `serve.go`, in `RunE` after `NewServer` succeeds:
    - `abs, aerr := filepath.Abs(tokenFile)`; on error `abs = tokenFile`.
    - `defer func() { _ = rt.Record.Remove(os.Getpid()) }()`.
    - The announce callback first does `rt.Record.Write(servestate.Record{PID: os.Getpid(), Listen: addr, TokenFile: abs, Started: time.Now().UTC()})`. On error it prints `fmt.Fprintf(cmd.ErrOrStderr(), "wt-model: note: could not write the runtime record (%s) — host commands cannot see this server's client sessions\n", err)`. Only then does it print `announceLine(addr)`.
  - Add one sentence to serve's `Long` before "Exit codes": "While it runs it keeps a runtime record, serve.json in the wt-model home (pid, address, token file path; never the token), and removes it when it stops cleanly."
  - Tests: change every `rootCmd(fakeDeps)` call site to `rootCmd(fakeDeps, testRuntime(t))`, and refactor `serveUntilListening` as in Interfaces.
- [ ] Step 4: Docs, same commit:
  - `wt-model-server.md` "Startup and shutdown": a paragraph on `~/.config/wt-model/serve.json`. It covers the fields, 0600 in a 0700 home, written before the listening line, removed on a clean stop (SIGINT/SIGTERM) only when it names this pid, and left behind by a kill (a later reader finds the address unreachable and says so). A second serve overwrites it.
  - `wt/CLAUDE.md` wt-model section: one sentence naming `internal/servestate` and the record.
  - `packages.md`: the `internal/servestate/` row (role, imports, "admin side; its importer is cmd/wt-model").
  - Add `servestate` to both ratchet lists in prose.
  - `CHANGELOG.md` Unreleased → Added: "`wt-model serve` keeps a runtime record, `~/.config/wt-model/serve.json` (pid, listen address, token file path, never the token; mode 0600), and removes it on a clean stop."
- [ ] Step 5: `cd wt && go test ./cmd/wt-model ./internal/servestate ./internal/removal ./internal/depcheck -count=1 && go vet ./... && make check`, then from the repo root `make check-links`. Expect PASS.
- [ ] Step 6: Commit `feat(wt-model): serve writes its runtime record and removes it on clean shutdown`. Files: `wt/cmd/wt-model/main.go`, `serve.go`, `serve_test.go`, `serve_record_test.go`, `litellm_test.go`, the other `cmd/wt-model/*_test.go` with a `rootCmd(` call, `wt/docs/wt-model-server.md`, `wt/docs/internals/packages.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`.

**4d-i PR body:** `Refs` the stage-2 tracking issue (never a closing keyword). Run the 4d-i exit gate.

---

### Task 58: the host session reader over the record (`cmd/wt-model`)

**Files:**
- Create: `wt/cmd/wt-model/stop_sessions.go`
- Create: `wt/cmd/wt-model/stop_sessions_test.go` (rewrites `wt/cmd/wt-session/host_sessions_test.go`; that file is deleted in Task 60)

**Interfaces:**
- Consumes: `servestate.Store.Read`, `servestate.Sessions` (Task 55/3); `modelping.Clean(s string) string` (spine §2).
- Produces: `func hostSessionReader(ctx context.Context, w io.Writer, store servestate.Store) func() map[string]int` (spine §11). The returned func reads once (`sync.Once`) and returns the same map on every call.
  - No record, meaning `Read` gives `(_, false, nil)`: nil, silently.
  - A `Read` error or a `Sessions` error: nil, plus exactly one line on `w`: `fmt.Fprintf(w, "wt-model: note: could not read client sessions from the model server (%s) — models they use are stopped without asking\n", modelping.Clean(err.Error()))`.
  - Success: the `Sessions` map.

- [ ] Step 1: Write the failing tests. `stop_sessions_test.go` carries its own copy of Task 56's `fakeServer` helper, since test files cannot cross packages, and a `recordFor(t, srv, token) servestate.Store` that writes a record naming the fake into `testRuntime(t).Record`.
  - `TestHostSessionReaderCountsFromTheCatalog`: rows `omlx/busy` with Sessions 2 and `omlx/idle` with Sessions 0. `read := hostSessionReader(t.Context(), &w, store)`. `read()` equals `map[string]int{"omlx/busy": 2}`. A second `read()` leaves `catalogHits.Load() == 1`, and `w.Len() == 0`.
  - `TestHostSessionReaderIsSilentWithNoRecord`: a store with no file, and also `servestate.Store{}`. Both give nil and `w.Len() == 0`.
  - `TestHostSessionReaderNotesAnUnreachableServer`: record a fake, then `srv.Close()`. The result is nil, `strings.HasPrefix(w.String(), "wt-model: note: could not read client sessions from the model server (")`, `strings.HasSuffix(w.String(), ") — models they use are stopped without asking\n")`, and `strings.Count(w.String(), "\n") == 1`.
  - `TestHostSessionReaderNotesAnUnreadableRecord`: `serve.json` holds `{"pid":`. The result is nil, and the note line names the record path.
  - `TestHostSessionReaderNeverPrintsTheToken`: the fake wants `right-token` and the token file holds `tok-secret-xyz`. The note contains neither string.
- [ ] Step 2: `cd wt && go test ./cmd/wt-model -run TestHostSessionReader -count=1`. Expect `undefined: hostSessionReader`.
- [ ] Step 3: Implement `stop_sessions.go` with the doc comment: "what `wt-model stop`'s stopSessions runs: the live server's client sessions per model, found through serve's runtime record (never through the session client, which this binary must not link)".
- [ ] Step 4: `cd wt && go test ./cmd/wt-model -run TestHostSessionReader -count=1 && go test ./internal/depcheck -count=1`. Expect PASS. The depcheck run proves `cmd/wt-model` still links nothing in `sessionBanned`. The note uses `modelping.Clean`, not `cleanup.Flatten`.
- [ ] Step 5: Commit `feat(wt-model): read client sessions for host stops through the serve record`. Files: `wt/cmd/wt-model/stop_sessions.go`, `stop_sessions_test.go`.

### Task 59: `wt-model stop <provider>|--all`

**Files:**
- Create: `wt/cmd/wt-model/stop.go`. This is a copy of `wt/cmd/wt-session/model_cmds.go`: the seam block :30-52, `promptStop` :54-68 only if 4a-ii did not already add an equivalent to cmd/wt-model (see Step 3), and :208-652 (`haltsWhole` through `stopImpact`). `stopCmd` is new and `hostSessionReader` is Task 58's. The wt-session originals are deleted in Task 60. Within one PR the final diff reads as a rename.
- Create: `wt/cmd/wt-model/stop_test.go`. These are copies of the stop tests and helpers of `wt/cmd/wt-session/model_cmds_test.go`, listed in Step 1.
- Create: `wt/cmd/wt-model/stop_loading_test.go`, a copy of `wt/cmd/wt-session/stop_loading_test.go` (all of it).
- Create: `wt/cmd/wt-model/stop_untrusted_test.go`, a copy of `wt/cmd/wt-session/stop_untrusted_test.go` (all of it).
- Modify: `wt/cmd/wt-model/main.go` (`rootCmd` adds `stopCmd(h, rt.Record)`, where `h` is the `*host` 3b's `rootCmd` builds)
- Modify: `wt/cmd/wt-model/testmain_test.go` (the stop seam stubs of `wt/cmd/wt-session/testmain_test.go:69-86`)
- Modify: `wt/cmd/wt-model/host_test.go` (Task 42, 4a-iii) by adding the "stop provider" row of `wt/cmd/wt-session/host_commands_test.go:136-147`
- Modify: the cmd/wt-model `TestHelpNamesSushiWhereItListsProviders` (it exists in cmd/wt-model only if 4a-ii moved the model half; otherwise create it in `stop_test.go`)

**Interfaces:**
- Consumes:
  - `hoststop.ReadStopStateSessions(cfg *config.Config, sessions map[string]int) StopState`, `hoststop.StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error`, `hoststop.ErrStopCancelled`, `hoststop.Candidate`, `hoststop.FamilyState`, `hoststop.StopState`.
  - `lifecycle.Stop(ctx context.Context, cfg *config.Config, providerID string) error` (`lifecycle/lifecycle.go:703`), `lifecycle.StopLoading(ctx context.Context, cfg *config.Config, providerID string, pid int) error` (`server_loading.go:70`), `lifecycle.WaitPendingRoutes()`, `lifecycle.CanStop`, `lifecycle.TenancyOf`, `lifecycle.StopInterruptedError`.
  - `cli.NoTerminalRefusal(err error, flag, state string) error`, `cli.Declined(state string) error`, `cli.AskOnTTY(open func() (*os.File, error), question string, noTTY error) (bool, error)`, `cli.ErrNoTerminal` (spine §8).
  - cmd/wt-model's `openTTY` seam (4a-ii model rm). (`startSignalCtx` is not consumed: no earlier task gives cmd/wt-model one — Task 42's `warm` uses `cmd.Context()` — so this task declares it, below.) Also `(*host).hostConfig() (*config.Config, error)` and `(*host).hostConfigErr() error` (3b `host.go`).
  - `hostSessionReader` (Task 58).
- Produces, all in `cmd/wt-model/stop.go`:
  - Seams: `var stopState func(*config.Config) hoststop.StopState`, `stopSessions func() map[string]int`, `stopEntries = hoststop.StopEntries`, `confirmStop func(string) (bool, error)`, `stopProvider = lifecycle.Stop`, `stopLoading = lifecycle.StopLoading`, `waitPendingRoutes = lifecycle.WaitPendingRoutes`. Add `waitPendingRoutes` only if cmd/wt-model lacks it. Also, copied with its comment from `cmd/wt-session/start.go:14-19` (the moved bodies call it at `model_cmds.go:411,597`, and the copied tests stub it — `model_cmds_test.go:586-590,699-700`, `stop_loading_test.go:179-182`, `stop_untrusted_test.go:195-196`): `var startSignalCtx = func() (context.Context, context.CancelFunc) { return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) }` (first check `grep -n 'startSignalCtx =' cmd/wt-model/*.go` finds none).
  - `func stopCmd(h *host, rec servestate.Store) *cobra.Command`.
  - `func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error` and `func runStopAll(out io.Writer, cfg *config.Config, yes bool) error`, with the same signatures as wt-session's :530 and :343.
  - Unchanged signatures for `haltsWhole`, `haltProviders`, `unreadShared`, `haltImpact`, `errLoadingUnknown`, `pidfileFindings`, `errCannotTell`, `unreadClause`, `haltProvider`, `notHalted`, `haltCancelled`, `stoppableProviders`, `stopArgError`, `withFamilyCollateral` and `stopImpact`.

- [ ] Step 1: Write the failing tests.
  - Copy into `stop_test.go`, with each top-level comment kept and `wt stop` reworded to `wt-model stop`:
    - The helpers `modelCmdConfig`, `cand`, `stubStop`, `stubStopState`, `stubHalt` and `runStopCmd`. `runStopCmd` builds `stopCmd(&host{hostSet: true, hostCfg: cfg}, servestate.Store{})`; use the field names of 3b's `host`.
    - The tests `TestStopProviderStopsAllItsModels`, `TestStopInvalidArgErrors`, `TestStopProviderNothingRunningIsNotAnError`, `TestStopInUseConfirms`, `TestStopFailurePropagates`, `TestStopUnknownProviderListsConfiguredStoppable`, `TestStopUnknownProviderErrorsBeforeProbe`, `TestStopInUseCountIsTotalAcrossModels`, `TestStopBareOmlxHaltsTheService`, `TestStopAllStopsEveryModelThenHaltsThePool`, `TestStopAllHaltsOneServicePerPool`, `TestStopAllWithNothingToStop`, `TestStopAllInUseConfirms`, `TestStopAllKeepsGoingAfterAFailure`, `TestStopAllCancelledModelLoopLeavesThePoolUp`, `TestStopAllCancelledHaltReportsCancelled`, `TestStopAllSettlesTheModelLoopsRestartBeforeAHalt`, `TestStopAllYesFlagSkipsTheQuestion`, `TestStopAllTakesNoArgument`, `TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp`, `TestStopBareOmlxCancelledReportsCancelled`, `TestStopAllFailurePrintsNoUsage` and `TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow` (`model_cmds_test.go:85-264, 408-786, 843-end` on main).
  - Copy `stop_loading_test.go` and `stop_untrusted_test.go` whole, `TestMainIsolatesEveryProcessPidfile` and `TestStopLoadingSeamFailsClosed` included. cmd/wt-model's TestMain already calls `lifecycle.IsolateProcessesForTest()` (`testmain_test.go:20`).
  - Assertion values that change in the copies (and only these):
    - `"wt: nothing running on mtplx\n"` becomes `"wt-model: nothing running on mtplx\n"`. Likewise `"wt: no running local models\n"` and `"wt: nothing running on ollama\n"` (`stop_untrusted_test.go:98,105,359,363,387,508`). The joined want at `stop_loading_test.go:239` becomes `"wt-model: nothing running on mtplx\nwt-model: " + stray + "\nwt-model: no running local models\nwt-model: " + stray + "\n"`.
    - `stop_loading_test.go:196`: `run "wt-model stop mtplx" again to confirm`.
    - `TestStopAllTakesNoArgument`: want `"wt-model stop --all takes no provider"`.
    - `TestStopAllFailurePrintsNoUsage`: the `--all ollama` case now wants **no** `Usage:` and the error `wt-model stop --all takes no provider (got "ollama")`.
    - `TestStopFailurePrintsNoUsage` (`stop_untrusted_test.go:407`): the `nosuch` case wants no `Usage:` and an error containing `unknown provider "nosuch"`. The no-argument case wants the error `wt-model stop needs a provider or --all; stop one model with 'wt stop <model>'` and no `Usage:`.
    - Every helper or test that called the session's `noTerminalRefusal`/`declined` asserts `exitcode.Of(err)` as before (3 and 3).
  - New tests in `stop_test.go`:
    - `TestStopRefusesAModelID` (comment: a model is the model server's to stop, from `wt`). Seed `stopState` to fail the test if called. `runStopCmd(modelCmdConfig(), "omlx/q")` gives an error whose text is exactly `"omlx/q" is a model id: wt-model stop stops a provider or --all; stop one model with 'wt stop omlx/q'`, and `exitcode.Of(err) == 1`. `--all omlx/q` gives the same error.
    - `TestStopNeedsAProviderOrAll`: `runStopCmd(modelCmdConfig())` gives exactly `wt-model stop needs a provider or --all; stop one model with 'wt stop <model>'`, and `stopState` is not called.
    - `TestStopAsksAboutSessionsTheRecordNames` (comment: the in-use question still fires with the server's sessions, read through the record). Use Task 58's `fakeServer` with row `ollama/a:1` at Sessions 2, and `store := recordFor(…)`.
      - Stub `stopState = func(cfg *config.Config) hoststop.StopState { return hoststop.ReadStopStateSessions(cfg, stopSessions()) }` over a stubbed inventory: use `stubStop` semantics but carry `Sessions` from `stopSessions()["ollama/a:1"]` into `cand("ollama","ollama/a:1","a:1", n)`.
      - Record `confirmStop`'s question. Run `stopCmd(h, store)` with `ollama`.
      - Assert the question is `ollama is in use by 2 live wt session(s) (ollama/a:1); stop anyway?`, the error has `exitcode.Of == 3` (declined), and nothing stopped.
    - `TestStopNotesAnUnreadableServerAndGoesOn`: record a closed fake and use `stubStop(t, nil)`. Stderr holds exactly one `wt-model: note: could not read client sessions from the model server (` line, stdout is `wt-model: nothing running on ollama\n`, and err is nil.
    - `TestStopRegistryRowErrorNamesTheRegistry`: a copy of `wt/cmd/wt-session/load_error_hint_test.go` `TestRegistryRowErrorNamesTheRegistry` (:302-355, same four registry bodies and wants).
      - It sets `XDG_CONFIG_HOME` to a temp dir, writes the registry to `<dir>/local-ai/registry.toml`, and sets `t.Setenv("WT_LITELLM_CONFIG", <dir>/config.yaml)` holding `model_list: []\n`.
      - Run `rootCmd(fakeDeps, testRuntime(t))` with `{"stop","omlx"}` and `{"stop","--all"}`. The error is exactly `"config error: " + want + " (fix the entry in " + registry + ")"`.
  - In the existing `TestHelpNamesSushiWhereItListsProviders` in cmd/wt-model, add the path `{"stop"}`; if the test does not exist, create it in `stop_test.go` with that one path.
  - The "stop provider" row in `host_test.go`: `stop ollama --yes` over the scratch home `host_test.go` builds (Task 42). Stub `stopProvider` and `stopEntries` to record. Output does not contain `unknown provider`, and the registry was read through `hostConfig`. This row's model-server assertion is that no record exists in `testRuntime(t)`, so nothing is asked.
- [ ] Step 2: `cd wt && go test ./cmd/wt-model -run 'TestStop|TestMainIsolates|TestHelpNamesSushi|TestHostCommands' -count=1`. Expect build failures: `undefined: stopCmd`, `runStop`, `runStopAll`, `stopState`, …
- [ ] Step 3: Implement `stop.go`.
  - **Moved bodies:** move them verbatim except for these edits. `noTerminalRefusal` becomes `cli.NoTerminalRefusal` and `declined` becomes `cli.Declined`. The `wt: ` prefix becomes `wt-model: ` in every `"wt: ` print of the moved bodies: :394, :399, :557, :559, :626 (`withFamilyCollateral`); confirm with `grep -n '"wt: ' cmd/wt-model/stop.go` → no hit. :497 says `run \"wt-model stop %s\" again to confirm`. Package-doc comments name `wt-model stop`.
  - **`confirmStop`:** `confirmStop = promptStop` when 4a-ii already gave cmd/wt-model a `promptStop` (model rm's `confirmRemove` uses one). Otherwise declare `func promptStop(question string) (bool, error) { return cli.AskOnTTY(openTTY, question, cli.ErrNoTerminal) }`, following the 4a-i convention for where `[y/N]` is printed. Never two copies.
  - **`stopState` default:** `func(cfg *config.Config) hoststop.StopState { return hoststop.ReadStopStateSessions(cfg, stopSessions()) }`. `stopSessions` defaults to `func() map[string]int { return nil }`.
  - **`stopCmd(h, rec)`:**
    - `Use: "stop <provider> | --all"`, `Short: "Stop every running model of a provider, or every local model"`, `Args: cobra.MaximumNArgs(1)`, `SilenceUsage: true`.
    - `Long`: today's :73-104 text minus the model-id, picker and model-server sentences, keeping every provider name (`ollama, omlx, omlx-6bit, mtplx, rec, sushi`) and every `"wt stop X"` → `"wt-model stop X"`. Then one paragraph: "It asks before stopping a model that client sessions of the running model server use (read through serve's runtime record; --yes answers; with no terminal and no --yes: exit 3, nothing stopped). With no server running there is nothing to ask. A model id is stopped with 'wt stop <model>'." Exit codes are unchanged from today.
    - `Example: "  wt-model stop ollama\n  wt-model stop omlx\n  wt-model stop --all --yes"`.
    - `RunE` order:
      1. `strings.Contains(arg, "/")` returns the model-id error (text in Step 1).
      2. `arg == "" && !all` returns the needs-a-target error.
      3. `cfg, _ := h.hostConfig()`; when `h.hostConfigErr()` is set, return `config.ConfigError(cerr)`.
      4. `stopSessions = hostSessionReader(cmd.Context(), cmd.ErrOrStderr(), rec)`.
      5. With `all`: a non-empty `arg` returns `fmt.Errorf("wt-model stop --all takes no provider (got %q)", arg)`; otherwise `runStopAll`.
      6. Otherwise `runStop`.
    - Flags: `--yes` "Stop models that client sessions of the running model server use, without asking". `--all` "Stop every running local model, then the omlx service and an mtplx wt cannot read or that is still loading".
  - **`main.go`:** `root.AddCommand(…, stopCmd(h, rt.Record))`.
  - **`testmain_test.go`:** copy the stubs of wt-session's `testmain_test.go:69-86` (`stopState`, `stopEntries`, `stopProvider`, `stopLoading`, `confirmStop`) with their comment ("no test may probe live servers or stop a real model").
- [ ] Step 4: `cd wt && go test ./cmd/wt-model -count=1 && go test ./internal/depcheck -count=1 && go vet ./cmd/wt-model && gofmt -l cmd/wt-model`. Expect PASS and no gofmt output. The depcheck run passes: hoststop's closure (`lifecycle`, `litellm`, `localmodels`, `profiles`, `catalog`, …) is off `sessionBanned`.
- [ ] Step 5: Commit `feat(wt-model): wt-model stop <provider>|--all with the record's client sessions`. Files: `wt/cmd/wt-model/stop.go`, `stop_test.go`, `stop_loading_test.go`, `stop_untrusted_test.go`, `main.go`, `testmain_test.go`, `host_test.go` (and the file holding cmd/wt-model's `TestHelpNamesSushiWhereItListsProviders`).

### Task 60: `wt stop <provider>|--all` becomes a pointer; the session copies go

**Files:**
- Modify: `wt/cmd/wt-session/model_cmds.go`:
  - Delete :30-68 (seams, `promptStop`), :162-207 (`hostSessionReader`) and :208-652 (`haltsWhole` … `stopImpact`).
  - Rewrite `stopCmd` :70-160.
  - Update the file comment :1-5.
  - Trim the imports (`hoststop`, `localmodels`, `modelapi`, `modelclient`, `cleanup`, `lifecycle`, `slices`, `sync`, `errors`, `io`, as they fall unused).
- Modify: `wt/cmd/wt-session/main.go:271` (root example line).
- Modify: `wt/cmd/wt-session/testmain_test.go:69-86`. Delete the stop seam stubs. Keep `lifecycle.IsolateProcessesForTest()` (5a removes the last lifecycle use).
- Modify: `wt/cmd/wt-session/model_cmds_test.go`. Delete the tests and helpers copied in Task 59. Delete the `{"stop"}` path of `TestHelpNamesSushiWhereItListsProviders`, and delete the test if no path is left. `TestNoHelpTextNamesModelman` stays.
- Delete: `wt/cmd/wt-session/stop_loading_test.go`, `stop_untrusted_test.go`, `host_sessions_test.go`.
- Delete: `wt/cmd/wt-session/host_commands_test.go` (its last row, "stop provider", now lives in cmd/wt-model). Move any of its helpers still called elsewhere (`grep -n 'hostHome\|execOn\|hostCfgOf\|hostLoadErrOf\|setHostLoadErr\|setHostCfgErr' wt/cmd/wt-session/*_test.go`) into `wt/cmd/wt-session/helpers_test.go`, except `setHostCfgErr`: it writes `a.hostCfgErr`, which Task 68 deletes, and its one caller (`commands_config_test.go:113`) was rewritten in Task 31; delete it. If the grep shows another `setHostCfgErr` caller, rewrite that test to build its error from real files (as Task 31 did) in this task.
- Modify: `wt/cmd/wt-session/stop_remote_test.go:125-163` (`TestStopProviderStaysOnTheHostPath`) and `:290-318` (`TestStopAllStaysOnTheHostPathWithAnEndpoint`, deleted).
- Modify: `wt/cmd/wt-session/error_line_test.go:121-155` (`TestStopKeepsUsageOnAMistake`).
- Modify: `wt/cmd/wt-session/load_error_hint_test.go:302-355`. Delete `TestRegistryRowErrorNamesTheRegistry` (it now lives in cmd/wt-model as `TestStopRegistryRowErrorNamesTheRegistry`).
- Modify: `wt/cmd/wt-session/moved_test.go` (4a-i), adding conditional rows to `TestMovedCommandsPointAtWtModel`.
- Callers: `benchmarks/mtplx-profile-matrix:69,222`, `benchmarks/mtplx-profile-matrix.md:16`, `bin/update/rec:7,84`, `bin/update/sushi:14,118`. `llmbench` needs nothing: its one stop is `wt stop <provider>/<name> --yes` (`llmbench/src/llmbench/wt_bridge.py:144`), a session call that stays.

**Interfaces:**
- Consumes: `movedError(old, new string) error` (spine §10, `cmd/wt-session/moved.go`); `runStopRemote`, `(*app).openModels`, `remoteExit` (unchanged).
- Produces: `stopCmd(a *app) *cobra.Command`, the same signature with the new behaviour below.

- [ ] Step 1: Write the failing tests.
  - **`TestMovedCommandsPointAtWtModel`:** add the conditional rows `wt stop ollama`, `wt stop --all` and `wt stop --all --yes`. Their wants are exactly `wt: stop ollama moved to wt-model — run 'wt-model stop ollama'; nothing was changed` and `wt: stop --all moved to wt-model — run 'wt-model stop --all'; nothing was changed` (for both `--all` rows). Each prints one line and exits 1, and config.toml and registry.toml are byte-unchanged (the table's existing checks).
  - **`TestStopProviderStaysOnTheHostPath` is replaced by `TestStopProviderAndAllPointAtWtModel`** (comment: a provider name or `--all` is wt-model's now; the session never reads a registry for it and never asks the server). Use the remote rig from :130. Cases: `{"bogus"}` gives `stop bogus moved to wt-model — run 'wt-model stop bogus'; nothing was changed`. `{"ollama", "--yes"}` gives the `stop ollama` text. `{"--all"}` and `{"--all", "omlx"}` give the `stop --all` text. For each:
    - `exitcode.Of(err) == 1`;
    - `r.a.hostSet == false`: set up the app without `hostCfg`, and `hostConfig` is never called;
    - `len(r.f.reqs()) == 0`.

    The model-id half is kept as it is today (`omlx/q` reaches `r.f.Stops`).
  - **`TestStopKeepsUsageOnAMistake` is replaced by `TestStopPointerShowsNoUsage`.** Same root construction as :133-137 with `stopCmd(&app{})`. Cases `{"stop","nosuch"}`, `{"stop","--all"}`, `{"stop","--all","x"}`: `reportExit(err, &buf) == 1`, exactly one `wt: ` line containing `moved to wt-model`, no `Error:`, no `Usage:`.
  - The `"stop bad arg count"` row of `TestOneErrorLineThroughTheRealTree` (:56) stays.
- [ ] Step 2: `cd wt && go test ./cmd/wt-session -run 'TestMovedCommandsPointAtWtModel|TestStopProviderAndAllPointAtWtModel|TestStopPointerShowsNoUsage' -count=1`. Expect FAIL: today `stop bogus` gives `unknown provider "bogus"`.
- [ ] Step 3: Implement the session `stopCmd`.
  - `Use: "stop [model]"`, `Short: "Stop a running local model through the model server"`.
  - `Long`: today's model-id, picker and model-server paragraphs (:73-104 minus the provider and `--all` text), plus: "A provider name or --all moved to wt-model: run 'wt-model stop <provider>' or 'wt-model stop --all' on the machine that runs the model server. wt prints that pointer and exits 1, stopping nothing."
  - `Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop\n  wt stop ollama/qwen3.8:27b-mlx --yes"`. Keep `usageOnMistakeAnnotation`.
  - `RunE`: with `--all`, `cmd.SilenceUsage = true` and return `movedError("stop --all", "stop --all")`. With `arg != "" && !strings.Contains(arg, "/")`, `cmd.SilenceUsage = true` and return `movedError("stop "+arg, "stop "+arg)`. Otherwise the remote path, unchanged (:121-129).
  - Flags: `--yes` "Answer the model server's question when the model is in use by other client sessions". `--all` stays registered: "Moved: prints a pointer to 'wt-model stop --all' and exits 1".
  - `main.go:271`: `"  wt stop [model]              # stop a local model (picker when omitted)\n" +`. Keep the comment column at the same width as its neighbours.
  - File comment :1-5: "`wt start` and `wt stop <model>` go through the model server (model_cmds_remote.go); a provider name or --all is `wt-model stop`'s (a pointer here)."
  - Callers:
    - `benchmarks/mtplx-profile-matrix`: `run wt-model stop mtplx > /dev/null 2>&1` at :69 and :222. Keep the `command -v` preflight (:74) naming `wt-model` once 4a-ii added it; if not, add `command -v wt-model > /dev/null` to that check and to its message.
    - `benchmarks/mtplx-profile-matrix.md:16`: `` `wt-model stop mtplx` ``.
    - `bin/update/rec:7,84` and `bin/update/sushi:14,118`: `` `wt-model stop rec` `` and `` `wt-model stop sushi` ``.
- [ ] Step 4: `cd wt && go test ./cmd/wt-session -run 'TestMoved|TestStop|TestOneErrorLine|TestNoHelpText|TestHelpNames|TestNoWtCommandIsHidden|TestEveryRunnableCommandSilencesUsage' -count=1`, then `cd wt && go build ./... && go vet ./... && make check`, then from the repo root `make lint-shell && bash benchmarks/mtplx-profile-matrix --model mtplx/x --profile profile/x --dry-run | grep -c 'wt-model stop mtplx'`. Expect PASS, and a count ≥ 1 (the dry run only echoes; if its prerequisites refuse before echoing, skip the dry run and rely on `make lint-shell`).
- [ ] Step 5: `cd wt && go test ./cmd/wt-session -count=1` (the full package, ~3.5 min). Expect PASS.
- [ ] Step 6: Commit `feat(wt)!: wt stop <provider>|--all points at wt-model stop`, with the body "BREAKING CHANGE: a provider name or --all prints a pointer to `wt-model stop` and exits 1; `wt stop <model>` is unchanged." Files: the Modify/Delete list above.

### Task 61: the engine's provider-stop hints name `wt-model stop`

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go:286` (`(*LoadingInstanceError).Error`)
- Modify: `wt/internal/lifecycle/omlxpool.go:37` (`(*unloadRefusedError).Error`) and `:200` (the mid-load refusal in the unload path)
- Test: `wt/internal/lifecycle/omlxpool_test.go:470` and `:725-726`; `wt/internal/lifecycle/lifecycle_test.go` (new assertion, below)

**Interfaces:**
- Consumes: nothing new.
- Produces: unchanged signatures. Only error texts change. These errors reach users through the model server's jobs and through `wt-model stop`.

- [ ] Step 1: Update the tests first.
  - `omlxpool_test.go:470`: `const want = "omlx is still loading B and will not unload it mid-load; `+"`wt-model stop omlx`"+` stops the service and the load with it"`.
  - `:725-726`: `strings.Count(msg, "wt-model stop omlx") != 1`, with the message's want naming `` `wt-model stop omlx` ``.
  - Add `TestLoadingInstanceErrorNamesWtModelStop` (comment: the hint must name the command that exists after the cut). `(&LoadingInstanceError{ProviderID: "mtplx", PID: 7}).Error()` equals ``mtplx already has an instance (pid 7) still loading; wait for it to finish, or run `wt-model stop mtplx` ``.
- [ ] Step 2: `cd wt && go test ./internal/lifecycle -run 'TestLoadingInstanceErrorNamesWtModelStop|TestOmlx' -count=1`. Expect FAIL on the three texts.
- [ ] Step 3: In the three format strings, replace `` `wt stop %s` `` and `` `wt stop omlx` `` with `` `wt-model stop %s` `` and `` `wt-model stop omlx` ``. Nothing else in the texts changes.
- [ ] Step 4: `cd wt && go test ./internal/lifecycle ./internal/hoststop -count=1`. Expect PASS.
- [ ] Step 5: Commit `fix(lifecycle): name wt-model stop in the provider-stop hints`. Files: `wt/internal/lifecycle/lifecycle.go`, `omlxpool.go`, `omlxpool_test.go`, `lifecycle_test.go`.

### Task 62: docs for `wt-model stop`

**Files:**
- `wt/docs/wt-start-stop.md`:
  - Title becomes "`wt start`, `wt stop` and `wt-model stop`".
  - The quick block :11-16 moves provider and `--all` lines to `wt-model stop …`.
  - :22-31 says provider and `--all` stops are `wt-model stop`, on the machine that runs the server.
  - `## wt stop [model|provider]` :127 splits into `## wt stop [model]` and `## wt-model stop <provider> | --all`, which carries every provider bullet of today's section.
  - "In-use confirmation" :301-313 is rewritten. `wt-model stop` asks (default No) about sessions the running server reports through serve's runtime record. With no record there is nothing to ask. With a record it cannot read, it prints the `wt-model: note: could not read client sessions …` line and goes on without asking.
  - Each pinned output string becomes the new text: `wt-model: nothing running on …`; ``run "wt-model stop mtplx" again to confirm`` (:182); `` `wt-model stop omlx` `` in quoted engine errors (:83, :519).
- `docs/guides/02-providers-and-models.md:225,268,381`
- `docs/guides/08-maintenance-and-troubleshooting.md:319,326,421,429,487,498`. In the command table, the `stop --all` row says `wt-model stop --all`, and "Where each command runs" names `wt-model stop`.
- `docs/guides/04-litellm-config.md:55,186,218,357`; `docs/guides/05-benchmarks.md:208,209`; `docs/guides/06-wt-agents-and-models.md:77` (becomes `wt stop [model]`, plus "`wt-model stop <provider>` on the server's machine"); `docs/guides/00-config-map.md:342`
- `docs/reference/provider-artifacts.md:233,275`
- `CLAUDE.md:31,62`; `wt/CLAUDE.md:55,149`
- `wt/docs/wt-agents/README.md:59`; `wt/docs/wt-session.md:240`; `wt/docs/internals/local-models.md:99-101` (now `cmd/wt-model/stop.go`); `wt/docs/internals/testing.md:83`; `wt/docs/internals/launch-flow.md:45`; `wt/docs/internals/packages.md:50` (hoststop's importer is `cmd/wt-model`'s `stop`)
- `wt/docs/wt-model-server.md` (the runtime-record paragraph of Task 57 adds "read by `wt-model stop`")
- `wt/CHANGELOG.md` Unreleased → Changed: "**Breaking:** `wt stop <provider>` and `wt stop --all` moved to `wt-model stop <provider>` and `wt-model stop --all` (run on the machine that runs the model server). They ask about client sessions of the running server through its runtime record. The old forms print a pointer and exit 1. `wt stop <model>` and the picker are unchanged."

**Interfaces:** none (docs only).

- [ ] Step 1: Run the finder below. It lists every doc and script line still naming the old forms, and the files above are its output on main:
  `git grep -nE 'wt stop (ollama|omlx|omlx-6bit|mtplx|rec|sushi|--all|<provider>)([^/[:alnum:]_-]|$)|wt stop \[model\|provider\]|wt stop <provider>' -- ':!docs/archive' ':!docs/superpowers' ':!wt/docs/superpowers' ':!wt/CHANGELOG.md' ':!*.go'`
- [ ] Step 2: Edit each hit to the `wt-model stop …` form. The rules:
  - Keep `wt stop <model>` / `wt stop <provider>/<name>` / bare `wt stop` (session commands).
  - No live model state.
  - Home paths as `~/.config/…` or `/Users/user_name`.
- [ ] Step 3: From the repo root, run `make check-links`. Expect PASS.
- [ ] Step 4: Run Step 1's grep again. Expect no output.
- [ ] Step 5: Commit `docs: wt-model stop for provider and --all stops`. Files: those listed.

### Task 63: the session binary's ratchet test

**Files:**
- Modify: `wt/internal/depcheck/depcheck_test.go`. Add `sessionBinaryAllow` and `TestSessionBinaryStaysOffTheAdminPackages`, and update the file's list comments.
- Modify: `wt/cmd/wt-session/boundary_test.go:29`. Add `"model_cmds"` to the session file list (after Task 60 it holds only `startCmd` and the session `stopCmd`).
- Docs: `wt/CLAUDE.md` "Import graph ratchet" :305-307; `wt/docs/internals/packages.md:91`.

**Interfaces:**
- Consumes: `closure(t, pkg)`, `ratchetProblems(pkg string, set map[string]bool, banned, allowedList []string) []string`, `adminBanned` (`depcheck_test.go:63,101,16`).
- Produces (spine §12): `var sessionBinaryAllow = []string{"internal/lifecycle", "internal/litellm", "internal/localmodels", "internal/spend"}`, with the comment "the admin packages cmd/wt-session still links: lifecycle (start.go's waitPendingRoutes, main.go's WaitPendingRoutes, the answerer's WithSessions) drags litellm and localmodels; spend is stats' types. It only shrinks; 5a empties it." Also `func TestSessionBinaryStaysOffTheAdminPackages(t *testing.T)`.

- [ ] Step 1: Before writing, run `cd wt && go list -deps ./cmd/wt-session | grep -E 'wt/internal/(lifecycle|litellm|localmodels|cloudsync|spend|modeladmin|configeditor|modelserver|modelgateway|modeljobs|sessionreg|candidates|hoststop|modelsetup|modeleditor|servestate)$'`. Expect exactly lifecycle, litellm, localmodels, spend. If anything else is listed, an earlier PR left an edge. Stop and report; do not widen the allow-list.
- [ ] Step 2: Write `TestSessionBinaryStaysOffTheAdminPackages` (comment: after the hard cut the session binary links no administration code but what the allow-list names, and the list only shrinks: a gone edge fails until deleted). Body: `for _, p := range ratchetProblems("cmd/wt-session", closure(t, "cmd/wt-session"), adminBanned, sessionBinaryAllow) { t.Error(p) }`. Add `"model_cmds"` to `boundary_test.go`'s list.
- [ ] Step 3: `cd wt && go test ./internal/depcheck -run TestSessionBinaryStaysOffTheAdminPackages -count=1 && go test ./cmd/wt-session -run TestSessionPathsNeverTouchTheRegistryOrLifecycle -count=1`. Expect PASS. If the boundary scan flags `model_cmds.go`, Task 60 left host code in it: fix that, not the scan.
- [ ] Step 4: Update the ratchet prose (CLAUDE.md, packages.md): "`cmd/wt-session` itself is held off the admin set except `sessionBinaryAllow` (lifecycle, litellm, localmodels, spend), emptied in stage 5". Then `cd wt && go vet ./internal/depcheck && make check`, then from the repo root `make check-links`.
- [ ] Step 5: Commit `test(depcheck): hold the wt-session binary off the admin packages`. Files: `wt/internal/depcheck/depcheck_test.go`, `wt/cmd/wt-session/boundary_test.go`, `wt/CLAUDE.md`, `wt/docs/internals/packages.md`.

**4d-ii PR gate:**
- `cd wt && go build ./... && go vet ./... && make check && go test ./... -count=1`
- `cd wt && go test ./internal/depcheck ./internal/removal ./internal/servestate ./internal/hoststop ./internal/lifecycle ./cmd/wt-model -count=1`
- From the repo root: `make lint-shell && make check-links`, then Task 62 Step 1's grep, which must print nothing.

PR body: `Refs` the tracking issue. It names the breaking change and the callers updated (mtplx-profile-matrix, bin/update/rec and sushi hints; llmbench unchanged because it stops model ids only).

---

## PR 5: close-out

**Entry:** PR 4d-ii merged (spine §13): every host command lives in `cmd/wt-model` behind a pointer in `cmd/wt-session/moved.go`; `internal/depcheck/depcheck_test.go` has `TestSessionBinaryStaysOffTheAdminPackages` with `sessionBinaryAllow = []string{"internal/lifecycle", "internal/litellm", "internal/localmodels", "internal/spend"}`; `internal/cli` exists (4a-i).
**Exit gate:** PR 5a (Task 64–69): `cd wt && go build ./... && go vet ./... && make check && go test -count=1 ./... && make single-p`, with `go test -count=1 ./internal/depcheck ./internal/removal` named in the PR body, then `../bin/check-config-dirs-untouched`. PR 5b (Task 70–76): the same, plus from the repo root `make lint`, `make test-all`, `(cd modeltiers && uv run pytest tests/test_guide_samples.py -q)` and the Task 75 sweep printing nothing.
**Split (spine §13):** 5a = code (Task 64–69, ~400 lines); 5b = docs, install and the sweep (Task 70–76). Line numbers below are from `origin/main` f60ad48a. PRs 1a–4d-ii move them, so find each spot by the symbol named beside it.

Rules for every task: run Go from `wt/`. Every new `Test*` gets a top-level `//` comment saying what it tests and why. Run gofmt and go vet clean. Run `cmd/wt-session` with `-run` subsets inside a task, and the whole package only at the PR gate (about 3.5 minutes). Write `/Users/user_name` for home paths in docs. PR bodies say `Refs #N`, never close/fix/resolve + `#N`; write them with `gh pr create --body-file`. The ratchet reads non-test imports only (`go list -deps` without `-test`), so a `_test.go` file in `cmd/wt-session` that imports an admin package (for example `start_others_test.go` → `lifecycle.OccupiedError`, `stats_usage_test.go` → `litellm.ErrNoDatabase`) is allowed and stays.

---

### Task 64: `wt stats` reads the wire spend types; `spend.Row`/`Result` become aliases

**Files:**
- Modify: `wt/internal/spend/spend.go:42-58`. `type Row struct{…}` becomes `type Row = modelapi.SpendRow` and `type Result struct{…}` becomes `type Result = modelapi.SpendReport`. Keep both doc comments, plus one sentence: "an alias of the wire type, so the server and `wt stats` share one definition". Add the import `internal/modelapi`; it is allowed because `modelapi` imports nothing internal.
- Modify: `wt/internal/modelserver/support.go:128-131`. The loop `rep.Rows = append(rep.Rows, modelapi.SpendRow(row))` becomes `rep.Rows = append(rep.Rows, res.Rows...)`. Keep the non-nil `Rows: []modelapi.SpendRow{}` start, so the JSON still says `"rows": []`.
- Modify: `wt/cmd/wt-session/stats_usage.go`:
  - Drop the import of `internal/spend` (`:20`).
  - Change `usageRow.Spend *spend.Row` to `*modelapi.SpendRow` (`:32`).
  - In `buildUsageRows`, change the parameter `sp *spend.Result` to `*modelapi.SpendReport` (`:126`), and change `&spend.Row{Model: id}` (`:133`) and `(spend.Row{Model: r.Model})` (`:180`) to the `modelapi.SpendRow` spellings.
  - In `collectUsageRemote` (`:283-287`), the copy loop becomes `return spendOutcome{status: spendOK, result: &res}`.
  - Change `spendOutcome.result *spend.Result` (`:314`) and `var sp *spend.Result` (`:325`) to `*modelapi.SpendReport`.
  - Comment `:123` "and spend.Query both leave such entries out" becomes "and the model server's spend both leave such entries out".
- Modify: `wt/cmd/wt-session/stats.go:106`. The comment "reading its spend through spend." becomes "reading its spend through the statsSpend source spend.".
- Modify (tests, mechanical; with the alias they compile either way, but session code then names one type): replace `spend.Result{…}` → `modelapi.SpendReport{…}` and `spend.Row{…}` → `modelapi.SpendRow{…}` in `wt/cmd/wt-session/stats_json_test.go` (`:42,90,123,148,180,274,437`), `stats_usage_test.go` (`:51,152,215,303,361,412,440,454,478,507,609,667`), `stats_usage_rows_test.go` (`:27,112,165,166,197,203`) and `stats_usage_table_test.go` (`:15,19-23,135`). `stubSpend(t *testing.T, res spend.Result, err error)` (`stats_usage_test.go:51`) becomes `stubSpend(t *testing.T, res modelapi.SpendReport, err error)`, and its copy loop (`:54-57`) becomes `f.Spend = res`. The error sentinels `spend.ErrNoPsql`, `spend.ErrUnreachable`, `spend.ErrNoConnectionString`, `spend.InWindow` and `litellm.ErrNoDatabase` stay; they are test-only imports.
- Modify: `wt/internal/depcheck/depcheck_test.go`: in `sessionBinaryAllow`, delete `"internal/spend"`.
- Test: `wt/internal/spend/spend_test.go` (new test, below).

**Interfaces:**
- Consumes: `modelapi.SpendRow{Model string; Requests, PromptTokens, CompletionTokens int64; Spend float64}` and `modelapi.SpendReport{Rows []SpendRow; Unattributed int64}` (`internal/modelapi/modelapi.go:214-228`); `(*modelclient.Client).Spend(ctx, start, end time.Time) (modelapi.SpendReport, error)` (`internal/modelclient/client.go:349`).
- Produces: `spend.Row = modelapi.SpendRow`, `spend.Result = modelapi.SpendReport` (aliases). `buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *modelapi.SpendReport, families map[string]string, f statsFilter) []usageRow`.

- [ ] **Step 1: Write the failing tests.**
  (a) In `depcheck_test.go`, remove `"internal/spend"` from `sessionBinaryAllow`.
  (b) Add to `wt/internal/spend/spend_test.go`:
  ```go
  // TestSpendTypesAreTheWireTypes pins spend.Row and spend.Result as aliases
  // of modelapi.SpendRow and SpendReport: wt stats reads the wire types, the
  // server hands Query's result on unconverted, and a field added to one side
  // only would otherwise drift silently.
  func TestSpendTypesAreTheWireTypes(t *testing.T) {
  	var r Row = modelapi.SpendRow{Model: "m", Requests: 2}
  	var res Result = modelapi.SpendReport{Rows: []modelapi.SpendRow{r}, Unattributed: 1}
  	if res.Rows[0].Model != "m" || res.Unattributed != 1 {
  		t.Fatalf("got %+v", res)
  	}
  }
  ```
- [ ] **Step 2: Run them and see them fail.**
  - `cd wt && go test -count=1 ./internal/depcheck -run TestSessionBinaryStaysOffTheAdminPackages` → FAIL: `cmd/wt-session links internal/spend, which the session library packages must not; remove the import edge (do not add it to the allow-list)`.
  - `go test -count=1 ./internal/spend -run TestSpendTypesAreTheWireTypes` → build failure: `cannot use modelapi.SpendRow{…} (value of struct type modelapi.SpendRow) as Row value`.
- [ ] **Step 3: Implement.** Make the alias edits in `spend.go`, the `support.go` append, and the `stats_usage.go`/`stats.go` and stats test edits listed under Files.
- [ ] **Step 4: Run the tests and expect PASS.**
  - `cd wt && go build ./... && go vet ./... && gofmt -l . | wc -l` prints `0`.
  - `go test -count=1 ./internal/spend ./internal/modelserver ./internal/depcheck`.
  - `go test -count=1 ./cmd/wt-session -run 'TestStats|TestBuildUsageRows|TestRenderUsageTable|TestSpendEndUsesServerTime|TestVisibleID|TestFormatCount'`.
  - `go test -count=1 ./internal/modelserver -run 'TestSpend'` covers `TestSpendReturnsRowsAndUnattributed`, `TestSpendBoundsAreRequired` and `TestSpendNotConfiguredVersusUnavailable`.
- [ ] **Step 5: Commit.** `refactor(wt): wt stats reads modelapi's spend types; spend.Row and Result alias them`. Files: `wt/internal/spend/spend.go`, `wt/internal/spend/spend_test.go`, `wt/internal/modelserver/support.go`, `wt/cmd/wt-session/stats_usage.go`, `wt/cmd/wt-session/stats.go`, the four stats test files, `wt/internal/depcheck/depcheck_test.go`.

### Task 65: `WithSessions` moves from `lifecycle` to `cli`

**Files:**
- Create: `wt/internal/cli/sessions.go` with `func WithSessions(s string, sessions int) string`. Copy it from `wt/internal/lifecycle/message.go:65-74`, doc comment included, with the same byte-identical output `"%s (in use by %d wt session(s))"`.
- Modify: `wt/internal/lifecycle/message.go:65-74`: delete `WithSessions`, and drop `fmt` if nothing else in the file uses it (the build tells you).
- Move test: cut `TestWithSessionsCountsOnlyWhenUsed` from `wt/internal/lifecycle/message_test.go:44-62` into `wt/internal/cli/sessions_test.go` (package `cli`), adding one case `{"omlx/a", -1, "omlx/a"}`.
- Modify: `wt/cmd/wt-session/answerer.go:181`: `lifecycle.WithSessions(line, counts[r.ID])` becomes `cli.WithSessions(line, counts[r.ID])`. Drop the import of `internal/lifecycle` from `answerer.go`; it was the file's only use.

**Interfaces:**
- Consumes: package `internal/cli` (4a-i, spine §8).
- Produces: `cli.WithSessions(s string, sessions int) string`.

- [ ] **Step 1: Write the failing test.** Create `wt/internal/cli/sessions_test.go` with the moved test:
  ```go
  // TestWithSessionsCountsOnlyWhenUsed verifies the one wording the start
  // prompts share for an occupant's session count (#412): the line alone when
  // nobody (or a negative count) uses the model, the count in parentheses after it otherwise.
  func TestWithSessionsCountsOnlyWhenUsed(t *testing.T) { /* table: {"omlx/a",0,"omlx/a"}, {"omlx/a",-1,"omlx/a"},
     {"omlx/a",2,"omlx/a (in use by 2 wt session(s))"}, {"omlx/a — 4.0 GB loaded",1,"omlx/a — 4.0 GB loaded (in use by 1 wt session(s))"} */ }
  ```
- [ ] **Step 2: Run it and see it fail.** `cd wt && go test -count=1 ./internal/cli -run TestWithSessionsCountsOnlyWhenUsed` → `undefined: WithSessions`.
- [ ] **Step 3: Implement.** Create `sessions.go`, delete the lifecycle copy and its test, and switch `answerer.go`. Before deleting, check that nothing else calls it: `git grep -n 'WithSessions' -- wt` must list only `internal/cli/sessions*.go` and `cmd/wt-session/answerer.go`.
- [ ] **Step 4: Run the tests and expect PASS.**
  - `cd wt && go build ./... && go vet ./... && go test -count=1 ./internal/cli ./internal/lifecycle`.
  - `go test -count=1 ./cmd/wt-session -run 'TestOthers|TestStartOthers|TestAnswer'`.
  - `go test -count=1 ./internal/depcheck` stays green: `cli` is in `sessionLibraries`, and lifecycle is still allowed for `cmd/wt-session` through `main.go`/`start.go`.
- [ ] **Step 5: Commit.** `refactor(wt): move WithSessions to internal/cli; the start answerer no longer links lifecycle`. Files: `wt/internal/cli/sessions.go`, `wt/internal/cli/sessions_test.go`, `wt/internal/lifecycle/message.go`, `wt/internal/lifecycle/message_test.go`, `wt/cmd/wt-session/answerer.go`.

### Task 66: wt-session stops waiting on route writes; the binary ratchet is empty

**Files:**
- Modify: `wt/cmd/wt-session/main.go:40-55`. `main` becomes `if err := rootCmd().Execute(); err != nil { os.Exit(reportExit(err, os.Stderr)) }`. Delete the eight-line comment and `lifecycle.WaitPendingRoutes()` (`:44-52`), and the `internal/lifecycle` import.
- Modify: `wt/cmd/wt-session/start.go:28-35`: delete the `waitPendingRoutes` seam and its comment, and the `internal/lifecycle` import.
- Modify: `wt/cmd/wt-session/boundary_test.go`:
  - Delete `"main.go:main:lifecycle.WaitPendingRoutes": true` from `boundaryAllowed` (`:171-173`) and its bullet in the comment (`:165-169`).
  - In `TestSessionPathsNeverTouchTheRegistryOrLifecycle`'s comment (`:176-188`), "start or route a model in process (lifecycle.Start, EnsureModelRoute, WaitPendingRoutes)" stays as is.
- Modify: `wt/internal/depcheck/depcheck_test.go`:
  - `var sessionBinaryAllow []string` becomes empty. Keep the "only shrinks" comment and add "(empty since phase 4 stage 2, PR 5a)".
  - Rewrite `TestSessionBinaryStaysOffTheAdminPackages`' comment: "cmd/wt-session links no admin package (adminBanned); the allow-list is empty and only shrinks".
- Modify: `wt/CLAUDE.md`:
  - "Lifecycle" bullet (`:150`): "Call `WaitPendingRoutes()` on every host process exit" becomes "…on every `wt-model` process exit (`cmd/wt-model/main.go`; `wt` writes no route and never waits)".
  - "Import graph ratchet" paragraph (`:305-307`): name the final lists from spine §12. Session libraries are `cleanup`, `agents`, `modelclient`, `smoke`, `profiles`, `catalog`, `survey`, `tui`, `urlrule`, `modelping`, `modelmode`, `editorkit`, `modeview`, `sessioneditor`, `cli`. Admin packages are `lifecycle`, `litellm`, `localmodels`, `cloudsync`, `spend`, `modeladmin`, `modeleditor`, `candidates`, `hoststop`, `modelsetup`, `servestate`, and the server packages. `cmd/wt-model` stays off the session half (`sessionBanned`, which includes `sessioneditor` and `modelclient`). **`cmd/wt-session` stays off every admin package** (`TestSessionBinaryStaysOffTheAdminPackages`, allow-list empty). `modeleditor`, `editorkit` and `sessioneditor` stay off `tui`. The leaves `modelping`/`modelmode`/`editorkit`/`modeview`/`cli` must not link `modelclient` (`TestModeLeavesStayOffTheClient`). Both allow-lists are empty and only shrink.
  - "Post-exit flow" bullet (`:55`): "A batch of host stops (`wt stop <provider>`/`--all`)" becomes "(`wt-model stop <provider>`/`--all`)".

**Interfaces:**
- Consumes: nothing new.
- Produces: `cmd/wt-session` links none of `adminBanned` (non-test closure).

- [ ] **Step 1: Write the failing test.** In `depcheck_test.go`, set `sessionBinaryAllow` to the empty list.
- [ ] **Step 2: Run it and see it fail.** `cd wt && go test -count=1 ./internal/depcheck -run TestSessionBinaryStaysOffTheAdminPackages` → FAIL with three lines: `cmd/wt-session links internal/lifecycle, …`, `cmd/wt-session links internal/litellm, …`, `cmd/wt-session links internal/localmodels, …`.
- [ ] **Step 3: Implement.** Make the `main.go` and `start.go` deletions and the `boundary_test.go` entry removal, then confirm nothing is left to remove:
  - from the repo root: `git grep -n 'waitPendingRoutes\|internal/lifecycle"\|internal/litellm"\|internal/localmodels"\|internal/spend"' -- 'wt/cmd/wt-session/*.go' ':!*_test.go'` → no output (scoped to the session binary: `cmd/wt-model` imports these packages by design).
  - from `wt/`: `go list -deps ./cmd/wt-session | grep -E 'internal/(lifecycle|litellm|localmodels|spend|cloudsync|modeladmin|candidates|hoststop|modelsetup|modeleditor|servestate|modelserver|modelgateway|modeljobs|sessionreg)$'` → no output.

  Any hit is an edge PR 4 left. Remove the edge; never add an allow-list entry. The route-sync wrappers that read `probeInventory` (`desiredRouteIDs`, `syncRoutesAfterWrite`, `runLitellmSync`) and the declaration itself were deleted in Tasks 31, 45 and 50; if one survived (a hit on `internal/localmodels"` or `internal/litellm"`), run Task 68's dead-identifier loop now and delete the dead ones in this task (list the files in the commit). If an edge cannot be removed inside this task, stop and report.
  Then make the `wt/CLAUDE.md` edits.
- [ ] **Step 4: Run the tests and expect PASS.**
  - `cd wt && go build ./... && go vet ./... && go test -count=1 ./internal/depcheck`.
  - `go test -count=1 ./cmd/wt-session -run 'TestSessionPathsNeverTouchTheRegistryOrLifecycle|TestBoundaryScanSeesOnlyCode|TestOneErrorLineThroughTheRealTree|TestEveryRunnableCommandSilencesUsage'`.
  - `TestBoundaryScanSeesOnlyCode`'s synthetic `lc.WaitPendingRoutes` line stays. It tests the scanner, not the tree.
- [ ] **Step 5: Commit.** `refactor(wt): wt-session writes no route, so drop WaitPendingRoutes; empty the session-binary ratchet`. Files: `wt/cmd/wt-session/main.go`, `wt/cmd/wt-session/start.go`, `wt/cmd/wt-session/boundary_test.go`, `wt/internal/depcheck/depcheck_test.go`, `wt/CLAUDE.md`.

### Task 67: the boundary scan covers every session file

The explicit file list in `boundarySessionFiles` (`boundary_test.go:26-58`) named the session files of a binary that also held host commands. No host command is left, so every non-test file in `cmd/wt-session` is a session file, and the new session leaves join the globs.

**Files:**
- Modify: `wt/cmd/wt-session/boundary_test.go`:
  - `boundarySessionFiles` (`:26-58`) becomes the glob `"*.go"` in `.` plus the existing globs (`tui`, `agents`, `smoke`, `profiles`, `modelclient`) and `filepath.Join("..", "..", "internal", X, "*.go")` for each X in `modelmode`, `modelping`, `urlrule`, `editorkit`, `modeview`, `sessioneditor`, `cli`. Keep the `_test.go` skip and the `models_tab` prefix skip. Keep the rule that a glob matching no file fails.
  - Rewrite its doc comment: "every non-test file of cmd/wt-session (no host command is left in it since phase 4 stage 2) and of the session packages …".
  - `boundaryAllowed` (`:170-173`) becomes exactly:
    ```go
    "app.go:loadHost:config.Load":           true,
    "cleanup.go:noRegistry:.hostConfig":     true,
    "cleanup.go:protectedPaths:.hostConfig": true,
    "stats.go:spendFromServer:.hostConfig":  true,
    ```
    Each comment bullet names the reason: noRegistry and protectedPaths are R10's best-effort protected-path read, and spendFromServer is stats' family fallback when no server answers (design "Shared helpers": hostConfig stays for the cleanup commands and stats).
- Modify: `wt/docs/internals/testing.md`: the paragraph naming the boundary scan's files now names "every non-test file of `cmd/wt-session`" and the four allowed reads.

**Interfaces:** none.

- [ ] **Step 1: Write the failing test.** Make the `boundarySessionFiles` glob change but leave `boundaryAllowed` as Task 66 left it (only the `config.Load` entry).
- [ ] **Step 2: Run it and see it fail.** `cd wt && go test -count=1 ./cmd/wt-session -run TestSessionPathsNeverTouchTheRegistryOrLifecycle` → FAIL with exactly three violations: `cleanup.go:<n>: noRegistry: .hostConfig`, `cleanup.go:<n>: protectedPaths: .hostConfig`, `stats.go:<n>: spendFromServer: .hostConfig`. A fourth violation is a real finding: an admin call left in a session file, or `provider.X` in one. Stop and report it. Do not allow it.
- [ ] **Step 3: Implement.** Add the three `boundaryAllowed` entries with their reasons and update `testing.md`.
- [ ] **Step 4: Run the tests and expect PASS.** `cd wt && go test -count=1 ./cmd/wt-session -run 'TestSessionPathsNeverTouchTheRegistryOrLifecycle|TestBoundaryScanSeesOnlyCode|TestProtectedPathsCheckResolvesTheImport'`.
- [ ] **Step 5: Commit.** `test(wt): the session boundary scan covers every wt-session file and the new session leaves`. Files: `wt/cmd/wt-session/boundary_test.go`, `wt/docs/internals/testing.md`.

### Task 68: remove the dead host code left in `cmd/wt-session`

**Files:**
- Modify: `wt/cmd/wt-session/app.go:195-256`:
  - Delete `hostConfigErr` (`:231-237`), the field `hostCfgErr` (`:212`), and in `loadHost` the `verr`/`cfg.Validate()` lines (`:248-252`); its assignment becomes `a.hostCfg, a.hostLoadErr, a.hostSet = cfg, err, true`.
  - Rewrite `hostConfig`'s comment (`:216-225`): "the host's config: config.Load, read once and cached; only protectedPaths and noRegistry (R10, the cleanup commands' protected-path read) and stats' family fallback (spendFromServer) read it, best-effort, on a machine that may have no registry".
  - The struct comment (`:203-209`) loses "hostCfgErr is that error or else the validation error, for the commands that need a valid registry".
- Modify: `wt/cmd/wt-session/boundary_test.go`:
  - Delete `"hostConfigErr": true` from `bannedMethods` (`:101`).
  - Delete `_ = a.hostConfigErr()` from `TestBoundaryScanSeesOnlyCode`'s source and the `"x.go:25: f: .hostConfigErr"` want line. The remaining line numbers do not shift: it is the last banned line before `_ = key`.
  - Delete `hostConfigErr` from the `boundaryViolations` comment and the test's comment.
- Delete every package-level identifier in `cmd/wt-session` that is now unreferenced outside its definition and `testmain_test.go`, and its `TestMain` stub with its comment. Find them with:
  ```bash
  cd wt && for id in probeInventory stubProbeInventory desiredRouteIDs runLitellmSync syncRoutesAfterWrite realSyncRoutesAfterWrite seedEnv ollamaCaps stopState stopEntries stopProvider stopSessions confirmStop promptStop liveStopState hostSessionReader configeditorRun; do
    printf '%s: ' "$id"; git grep -lw "$id" -- cmd/wt-session | tr '\n' ' '; echo; done
  ```
  The rule for one row: a row that lists no file, or only its defining file plus `testmain_test.go`, is dead; delete the definition and the stub. A row that lists any other file stays. The loop is scoped to `cmd/wt-session` on purpose: `cmd/wt-model` declares several of the same names (`probeInventory`, `seedEnv`, `ollamaCaps`, `syncRoutesAfterWrite`, `realSyncRoutesAfterWrite`, `promptStop`, Task 25's `seams.go` and Task 59) by design; never widen the pathspec to `cmd` or `wt`.
  Drop the `testmain_test.go` imports that become unused (`hoststop`, `litellm`, `localmodels` when nothing else uses them; the build says which). Keep `lifecycle.IsolateProcessesForTest()` in `TestMain`: it is a defense, and test imports are outside the ratchet.

**Interfaces:**
- Produces: `(*app).hostConfig() (*config.Config, error)`. Its signature is unchanged; it is the one host read left.

- [ ] **Step 1: Write the failing test.** None new; this is a deletion. The guard is that the build and the existing tests still pass, and that, from the repo root, `git grep -n 'hostConfigErr\|hostCfgErr' -- wt/cmd/wt-session` prints nothing afterwards (before this task it prints only `app.go` and `boundary_test.go` lines). The grep is limited to the session binary on purpose: `cmd/wt-model`'s `host` (Task 25's `host.go`) keeps the field `hostCfgErr` and the method `hostConfigErr()` by design (`model_tui.go` and `stop.go` call `h.hostConfigErr()`, and the moved sync tests build `&host{… hostCfgErr: …}`); never delete or rename them there.
- [ ] **Step 2: Run the pre-check.** From the repo root, `git grep -n 'hostConfigErr\|hostCfgErr' -- wt/cmd/wt-session` lists only `app.go:2xx` and `boundary_test.go` lines (hits under `wt/cmd/wt-model` are expected and out of scope; see Step 1). Tasks 31, 51 and 60 removed the test-only users (`setHostCfgErr`, `TestLitellmRoutingCommandsWorkOnValidationOnlyError`'s fixture check, `TestConfigCmd_InvalidConfig_LaunchesEditor`). A remaining `_test.go` hit outside `boundary_test.go` is a test-only use: delete it (or rewrite it over `config.Load` + `Validate`, as Task 51 did) in this task and add its file to the commit. A non-test caller outside `app.go` is a host path PR 4 left; stop and report.
- [ ] **Step 3: Implement.** Make the deletions above.
- [ ] **Step 4: Run the tests and expect PASS.**
  - `cd wt && go build ./... && go vet ./... && gofmt -l cmd/wt-session | wc -l` prints `0`.
  - `go test -count=1 ./cmd/wt-session -run 'TestBoundaryScanSeesOnlyCode|TestSessionPathsNeverTouchTheRegistryOrLifecycle|TestStats|TestCleanup|TestProtectedPaths|TestNoRegistry'`.
  - from the repo root: `git grep -n 'hostConfigErr\|hostCfgErr' -- wt/cmd/wt-session` → no output (`wt/cmd/wt-model` still has them, by design).
- [ ] **Step 5: Commit.** `refactor(wt): delete wt-session's host leftovers (hostConfigErr, unused host seams)`. Files: `wt/cmd/wt-session/app.go`, `wt/cmd/wt-session/boundary_test.go`, `wt/cmd/wt-session/testmain_test.go`, each file a deleted seam lived in, and any `_test.go` Step 2's test-only cleanup touched (`helpers_test.go`, `litellm_test.go` if a use survived).

### Task 69: re-base the vacuity floors; a twin floor in `cmd/wt-model`

**Files:**
- Modify: `wt/cmd/wt-session/error_line_test.go:85-117` (`TestEveryRunnableCommandSilencesUsage`):
  - In the walk, a command carrying `removedCmdAnnotation` (declared in `cmd/wt-session/moved.go` since Task 36; moved stubs and retired ones) is still checked for `SilenceUsage` but not counted in `seen`.
  - `if seen < 20` becomes `if seen < sessionRunnableFloor`, with a test-file const `sessionRunnableFloor = <N1>` and the comment: "the runnable non-stub commands wt-session has after phase 4 stage 2; lower it only when a command leaves on purpose".
- Modify: `wt/cmd/wt-session/model_cmds_test.go:784-817` (`TestNoHelpTextNamesModelman`; if PR 4 moved it, edit it where it now lives). Don't count annotated stubs. `if seen < 20` becomes `if seen < sessionCommandFloor` (const `<N2>`, same comment); the failure text becomes `"walked %d commands, want at least %d"`.
- Create: `wt/cmd/wt-model/help_test.go` with:
  ```go
  // staleNameRE matches an admin command written under its old wt name.
  var staleNameRE = regexp.MustCompile(`(^|[^-\w/])wt (model( (init|list|add|edit|rm))?|cloud-sync|litellm (sync|list|providers|restart)|served|warm|stop (--all|<provider>|omlx|ollama|mtplx|rec|sushi|mlx_lm_server|llamacpp))([^/\w-]|$)`)
  // staleHelpHits returns each stale old-name use in text; `wt model list --server` (a session command) is not one.
  func staleHelpHits(text string) []string
  ```
  - `TestStaleHelpHitsSeesOnlyOldAdminNames`: hits for `"run wt model add x"`, `"wt cloud-sync --dry-run"`, `"wt stop omlx"`, `"wt litellm restart"`, `"bare wt model opens it"`. None for `"wt model list --server"`, `"wt-model model add"`, `"wt stop <model>"`, `"wt stop omlx/gemma"`, `"wt models"`, `"wt litellm status"`.
  - `TestWtModelHelpNamesNoOldCommand` walks `rootCmd(fakeDeps, runtime{Record: servestate.Store{Path: filepath.Join(t.TempDir(), "serve.json")}})`, using the root signature from 4d-i. For each command it reads `Short`, `Long`, `Example` and `UsageString()`, then each flag's `Usage` from `Flags()` and `PersistentFlags()`, and calls `t.Errorf("%s: help names an old wt command: %q", path, hit)` for each hit. It ends with `if seen < wtModelCommandFloor { t.Fatalf("walked %d commands, want at least %d", seen, wtModelCommandFloor) }`, where `const wtModelCommandFloor = <N3>`.

**Interfaces:**
- Consumes: `rootCmd(mk depsFactory, rt runtime) *cobra.Command`, `runtime{Record servestate.Store}` (4d-i), `fakeDeps` (`cmd/wt-model/serve_test.go`), `removedCmdAnnotation` (`cmd/wt-session/moved.go`, where Task 36 put it).
- Produces: test-only consts `sessionRunnableFloor`, `sessionCommandFloor`, `wtModelCommandFloor`; `staleHelpHits(text string) []string` (cmd/wt-model tests only).

- [ ] **Step 1: Write the failing test.** Create `help_test.go` with `TestStaleHelpHitsSeesOnlyOldAdminNames` and `TestWtModelHelpNamesNoOldCommand`, but not `staleHelpHits` yet. In the two wt-session walks, add the stub exclusion and a temporary `t.Logf("counted %d", seen)`.
- [ ] **Step 2: Run it and see it fail.**
  - `cd wt && go test -count=1 ./cmd/wt-model -run 'TestStaleHelpHits|TestWtModelHelpNamesNoOldCommand'` → build failure `undefined: staleHelpHits`.
  - Then implement `staleHelpHits`: `FindAllStringIndex`, skipping a match whose remaining text starts with ` --server`. Run again: `TestStaleHelpHits…` passes. `TestWtModelHelpNamesNoOldCommand` either passes or lists each stale help line PR 4 left; fix each in the command's own file, by the Task 75 mapping table.
  - Read the counts: `go test -count=1 ./cmd/wt-session -run 'TestEveryRunnableCommandSilencesUsage|TestNoHelpTextNamesModelman' -v | grep counted`, and the same for `./cmd/wt-model -run TestWtModelHelpNamesNoOldCommand` (add the temporary `t.Logf` there too).
- [ ] **Step 3: Implement.** Set `<N1>`, `<N2>` and `<N3>` to the three counted values, delete the three `t.Logf` lines, and record the values in the commit body.
- [ ] **Step 4: Run the tests and expect PASS.** `cd wt && go test -count=1 ./cmd/wt-model && go test -count=1 ./cmd/wt-session -run 'TestEveryRunnableCommandSilencesUsage|TestNoHelpTextNamesModelman|TestMovedCommandsPointAtWtModel|TestNoWtCommandIsHidden'`. Then run the PR 5a gate from the Entry/Exit block in the background, and read its exit status from the log, not from the wrapper.
- [ ] **Step 5: Commit, then open PR 5a.** `test(wt): re-base the command-tree floors after the cut; wt-model help names no old wt command` (body: `sessionRunnableFloor=<N1>, sessionCommandFloor=<N2>, wtModelCommandFloor=<N3>`). Files: `wt/cmd/wt-session/error_line_test.go`, `wt/cmd/wt-session/model_cmds_test.go`, `wt/cmd/wt-model/help_test.go`, plus any help text fixed. Open PR 5a: title `refactor(wt): phase 4 stage 2 close-out (5a): cut wt-session loose from spend and lifecycle`. Body: what moved, the gate commands and results, `Refs` to the phase-4 issue(s).

---

### Task 70: install, update and Makefile name `wt-model`

**Files:**
- Check only (made in Task 41, PR 4a-ii): `bin/update/wt`, as follows:
  - Header comment (`:5-7`): "it builds to wt/bin/wt-session, a copy wt/bin/wt, and wt/bin/wt-model".
  - Add `INSTALLED_MODEL="$HOME/.local/bin/wt-model"`. "Not installed" means none of the three names is present; the trailer becomes `no binary at $INSTALLED, $INSTALLED_SESSION or $INSTALLED_MODEL (run: make -C wt install)`. `INSTALLED_BIN` is the first present of wt, wt-session and wt-model.
  - `MISSING_NAME` becomes `MISSING_NAMES`, a space-joined list in that order. There is one `update_note "stale: no binary at <path>"` per missing name. The `--check` trailer becomes `stale "$installed_version installed, no binary at <paths joined by ', '> (run: make -C wt install)"`, and the update trailer says `(added <basenames joined by ', '>)`.
  - `find_stale_src` is unchanged (`cmd/` covers `cmd/wt-model`).
- Modify: `Makefile:60`: `install: ## Install all monorepo components (wt, wt-session and wt-model + llmbench + modeltiers)`.
- Modify: `wt/Makefile`:
  - `help` line 6: `install   - Copy bin/* (wt, wt-session, wt-model and the shims) to ~/.local/bin/`.
  - `single-p` (`:89`): check only. Task 12 added `./internal/modelmode` and Task 31 replaced `./internal/configeditor` with `./internal/sessioneditor ./internal/modeleditor`; confirm the list names no deleted package and includes those three. No edit is expected here.
  - `test` (`:98-115`): after the `wt config` smoke, add `Testing wt-model --help...` (`$(BINDIR)/wt-model --help >/dev/null || { echo "  wt-model --help: FAILED"; exit 1; }`) and the same for `wt-model setup --help` and `wt-model model --help`.
- Modify: `wt/CLAUDE.md` "Installation" (`:15-19`): "`make install` builds `bin/wt-session`, `bin/wt` (the same binary under two names) and `bin/wt-model` (the model server and every host-admin command) and copies them …".
- Check only (made in Task 41): `docs/guides/08-maintenance-and-troubleshooting.md:346`: the `bin/update/wt` paragraph says a missing `wt-model` also reads as stale.

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Use a scratch HOME with only the two session names:
  ```bash
  S=/private/tmp/claude-501/-Users-keith-github-ohanaverse-local-ai-setup/d909167d-4749-46b9-a55e-39439a049147/scratchpad/update-wt-home
  rm -rf "$S" && mkdir -p "$S/.local/bin" && for n in wt wt-session; do printf '#!/bin/sh\necho "wt 9.9.9"\n' > "$S/.local/bin/$n"; chmod +x "$S/.local/bin/$n"; done
  HOME="$S" bin/update/wt --check | tail -1
  ```
- [ ] **Step 2: Run it.** Since Task 41 it already prints `[update:wt] stale — wt 9.9.9 installed, no binary at <S>/.local/bin/wt-model (run: make -C wt install)`; if it prints `current`, Task 41's part was lost: make it here. The failing check of this task is `grep -n 'wt-model' Makefile` → no line (the root help does not name it) and `make -n -C wt test | grep -c wt-model` → 0.
- [ ] **Step 3: Implement.** Make both Makefiles' and `wt/CLAUDE.md`'s edits.
- [ ] **Step 4: Run the tests and expect PASS.**
  - Repeat Step 1; the last line is the stale trailer naming `wt-model`, exit 0.
  - Add a fake `wt-model` and rerun; it prints `[update:wt] current — wt 9.9.9`.
  - Remove all three; it prints `[update:wt] not-installed — no binary at …`, exit 3.
  - `make lint-shell`. `make help | grep wt-model` shows the install line.
  - `cd wt && make build && ls bin/wt bin/wt-session bin/wt-model && make -n test | grep -c 'wt-model'` (≥ 3) `&& make single-p`.
  - `go list ./internal/configeditor` fails ("no such package"). `grep -c configeditor wt/Makefile` → `0`.
- [ ] **Step 5: Commit.** `build: make install and the smoke test know wt-model`. Files: `Makefile`, `wt/Makefile`, `wt/CLAUDE.md` (plus `bin/update/wt` and guide 08 only if Step 2 found Task 41's part missing).

### Task 71: user guides 02, 04, 06, 08, 13, 14 and the root README

The rule for every edit is the Task 75 mapping table. Guides never embed live model state, and the home placeholder is `/Users/user_name`.

**Files:**
- Modify: `docs/guides/02-providers-and-models.md`:
  - Title (`:1`) becomes "Providers and models — `wt-model model`, registry.toml, and LiteLLM routing".
  - The prerequisites (`:10`) name `wt-model` on the host.
  - TL;DR block (`:27-33`): every `wt model …`/`wt litellm …` becomes `wt-model …`, and `wt model` (the Models tab) becomes `wt-model model`.
  - "Who does what" (`:40`) becomes: "**wt-model** = the registry, the Models tab (`wt-model model`), cloud-sync, LiteLLM routes, provider stop; **wt** = sessions".
  - Each Step's commands follow the same rule.
- Modify: `docs/guides/04-litellm-config.md:3,29-38,45,55,63,126` and every other `wt litellm sync|list|restart` and `wt model rm`: use the `wt-model` names. "`d` on its row in the Models tab of `wt config`" becomes "…of `wt-model model`".
- Modify: `docs/guides/06-wt-agents-and-models.md:101,131-144,337`:
  - Registry commands become `wt-model model …` and routes `wt-model litellm …`.
  - `:337` "The Agents tab of `wt config` never writes" stays; any mention of a Models tab there names `wt-model model`.
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`:
  - `:97,138,145,151,158-159,214` and every `wt litellm …`/`wt model list`/`wt stop <provider>` → `wt-model …`.
  - §6 "Where modelman's commands went" (`:476-510`): the table's right column gives the `wt-model` command where the command moved.
  - The "Where each command runs" paragraph (`:498`) becomes: "Host commands are `wt-model`'s: `wt-model model …`, `wt-model cloud-sync`, `wt-model litellm …`, `wt-model served|warm`, `wt-model stop <provider>|--all`, `wt-model setup`, and the Models tab of `wt-model model`. They read and write `registry.toml`, LiteLLM's `config.yaml` and `slot-profiles.toml` on the machine they run on; run them where `wt-model serve` runs. Everything else is a session command (`wt`) …". Delete the "Interim limit …" sentence.
- Modify: `docs/guides/13-deployment-use-cases.md`:
  - `:32`: "The `wt config` Model server tab does most of this" becomes "`wt-model setup` does most of this for `local`; `wt config`'s Mode tab sets `native` or `remote` on a session machine".
  - `:82,124`: host commands become `wt-model model add|edit|rm|init`, `wt-model cloud-sync`, `wt-model litellm …`.
  - `wt model list --server` lines (`:27,115`) stay.
- Modify: `docs/guides/14-litellm-install-and-configuration.md`:
  - `:5,74,78,145,200,203,233,263`: `wt litellm restart|list` become `wt-model litellm …`. Keep the `> Verified against:` date unless you re-run every edited command.
  - `## What the wt config Model server tab does for LiteLLM` (`:241-243`) becomes `## What wt-model setup does for LiteLLM`, with "The Setup tab of `wt-model setup`" in the body.
- Modify: `README.md:26` (the guide-04 row): `wt litellm sync` becomes `wt-model litellm sync`.

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Run the Task 75 sweep command restricted to these files: append `-- docs/guides/0[2468]-*.md docs/guides/1[34]-*.md README.md` in place of the pathspec. It lists the current hits; make a note of the count.
- [ ] **Step 2: Run it and see it fail.** It prints at least one line per file above.
- [ ] **Step 3: Implement.** Make the edits.
- [ ] **Step 4: Run the tests and expect PASS.**
  - The restricted sweep prints nothing.
  - `make check-links`.
  - `(cd modeltiers && uv run pytest tests/test_guide_samples.py -q)` passes (it pins guides 00 and 12).
  - `grep -nE '[0-9]+ models? (are|is) (routed|running)' docs/guides/0[2468]-*.md docs/guides/1[34]-*.md` → nothing (no live state).
- [ ] **Step 5: Commit.** `docs(guides): name wt-model for the host commands in guides 02, 04, 06, 08, 13, 14`. Files: the six guides, `README.md`.

### Task 72: wt's user pages (`wt-model-server.md`, `wt-session.md`, `wt-config.md`, `configuration.md`, `wt/README.md`)

**Files:**
- Modify: `wt/docs/wt-model-server.md`:
  - Intro (`:3-10`): "It has `wt-model serve`, the host-admin commands (`model`, `cloud-sync`, `litellm`, `served`, `warm`, `stop <provider>|--all`) and `wt-model setup`." Delete "`wt model`, with a space, is the registry command" and link [wt-model.md](wt-model.md) as "the `wt-model model` registry commands".
  - Add a section `## Commands` after the intro: a table with columns Command | What | Page. It has one row each for `serve`, `setup` (TUI, Setup tab: local setup, LaunchAgent, token, LiteLLM pair and plist actions), `model` (bare TUI: Models tab), `model init|list|add|edit|rm`, `cloud-sync`, `litellm sync|list|providers|status|set|restart`, `served <provider>`, `warm <provider> <model>`, and `stop <provider>|--all` (`stop <model>` is `wt stop`, a usage error here). Bare `wt-model` prints help.
  - `## Setting it up` (`:29-44`): "The way in is `wt-model setup` (the Setup tab: local or native) …; a session machine chooses `remote` (or `native`) on the Mode tab of `wt config` ([wt-config.md](wt-config.md), "Mode tab")."
  - Add to `## Startup and shutdown` (`:330`): the runtime record `~/.config/wt-model/serve.json` (mode 0600; pid, listen, token-file path, never the token; written after the banner, removed on clean exit only when it names this pid). Explain that `wt-model stop <provider>|--all` reads it to ask the live server for client sessions, and what the stale-record note says.
  - `## Known limits` (`:346-353`): `wt model` and `wt cloud-sync` become `wt-model model` and `wt-model cloud-sync`.
- Modify: `wt/docs/wt-session.md`:
  - `:14,26,91-112`: "Model server tab" becomes "Mode tab". Every quoted refusal must match the 3c text exactly; copy it from `internal/modelclient` with `git grep -n 'Mode tab' -- wt/internal/modelclient`.
  - `## Host commands` (`:234-243`) becomes `## Moved to wt-model`: one sentence ("the old names print one line and exit 1, changing nothing: `wt: <old> moved to wt-model — run 'wt-model <new>'; nothing was changed`"), then a two-column table with rows written exactly as `| \`wt model add\` | \`wt-model model add\` |`. There is one row per `movedCommands` entry and per conditional pointer (bare `wt model`, plain `wt model list`, `wt stop <provider>`, `wt stop --all`). These rows are the sweep's one allowed hit.
  - Keep the sentence on `wt worktrees`/`wt remove` reading the registry.
  - `## \`[litellm]\` on a session` (`:245-279`): the closing sentence "only the Model server tab of `wt config` does" becomes "only `wt config`'s Mode tab (and `wt-model setup` for local) does".
  - `## Interim limits` (`:281-288`): delete the "Host commands still run here." bullet.
- Modify: `wt/docs/wt-config.md`:
  - `:5`: "interactive TUI with two tabs: Agents and Mode".
  - Delete the Models-tab bullet (`:21-25`); it points to `wt-model model`.
  - `### Model server tab` (`:33-78`) becomes `### Mode tab`: native, remote and local-pointer only, as built in 3c. Cover the mode choice `native local remote`, the url, the token file or paste (remote), check, plan, `Apply this plan? [y/N]`, and local storing the defaults (`http://127.0.0.1:8787`, `~/.config/wt-model/server.token`); a stored url and token file are kept only when the stored mode is already `local` (switching from remote writes the defaults, never the remote url). Leaving local never touches the LaunchAgent ("remove it with `wt-model setup`"). When `wt-model` is on PATH it offers to run `wt-model setup`.
  - `:141` (the Models tab note) is deleted; `:155` "written by the Model server tab alone" becomes "by the Mode tab and `wt-model setup`".
- Modify: `wt/docs/configuration.md:116` and any "three tabs" text: two tabs (Agents, Mode), plus a pointer to `wt-model setup` and `wt-model model`.
- Modify: `wt/README.md:104,107,110,126`:
  - Rows `wt served`, `wt warm`, `wt model`, `wt cloud-sync` move to one row `wt-model …` | "Host administration: registry, routes, cloud sync, provider stop, local setup" | [docs/wt-model-server.md](docs/wt-model-server.md).
  - `wt start`/`wt stop` stay.
  - `:126` "three tabs" becomes "two tabs: Agents and Mode".

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Run the Task 75 sweep restricted to `-- wt/docs/wt-model-server.md wt/docs/wt-session.md wt/docs/wt-config.md wt/docs/configuration.md wt/README.md`.
- [ ] **Step 2: Run it and see it fail.** It lists hits in each file.
- [ ] **Step 3: Implement.** Make the edits.
- [ ] **Step 4: Run the tests and expect PASS.**
  - The restricted sweep prints nothing; the moved-commands table rows are filtered by the sweep's last `grep -v`.
  - `make check-links`.
  - `cd wt && go test -count=1 ./cmd/wt-session -run TestMovedCommandsPointAtWtModel`, then compare its table against the doc's rows: the row count equals `len(movedCommands)` + 4.
- [ ] **Step 5: Commit.** `docs(wt): wt-model's command page, the Mode tab and the moved-commands table`. Files: the five pages.

### Task 73: internals pages and both CLAUDE.md files

**Files:**
- Modify: `wt/docs/internals/packages.md`:
  - The file table (`:12,21,23,31-33` and the other rows naming moved files): `cmd/wt-session` rows list only what stays. Add `moved.go` (pointer stubs) and the `stats*` files reading `modelapi` spend. New `cmd/wt-model` rows: `host.go`, `model*.go`, `cloudsync*.go`, `litellm.go`, `served`/`warm`, `stop`, `setup`, `model_tui`.
  - Add one row per new package: `internal/urlrule`, `modelping`, `modelmode`, `editorkit`, `modeview`, `sessioneditor`, `modeleditor`, `cli`, `servestate`. Each row says its role and its allowed imports, from spine §12.
  - Delete the `internal/configeditor` row.
  - "Package graph rules" (`:87-…`): the table of spine §12 and the two empty, shrink-only allow-lists.
- Modify: `wt/docs/internals/config-and-registry.md:11,13,25,47`:
  - The concurrent writers of `config.toml` are the `wt config` editor (Agents, Mode) and `wt-model setup`.
  - `ErrRegistryMissing` names `wt-model model init`.
  - "`wt model init` creates" becomes "`wt-model model init` creates".
- Modify: `wt/docs/internals/tui.md:33-37`: "`wt config` is `internal/sessioneditor`, `wt-model model`/`setup` are `internal/modeleditor`, both on `internal/editorkit`; neither links `internal/tui`". The Model server tab paragraph becomes the Mode tab (`sessioneditor/mode_tab.go`) and the Setup tab (`modeleditor/setup_tab.go`).
- Modify: `wt/docs/internals/launch-flow.md:29,45,47`: where the old names appear, the first-run gate opens the Mode tab, and stop names `wt-model stop`.
- Modify: `wt/docs/internals/local-models.md:16` and others: `wt served` (`cmd/wt-session/model_cmds.go` `runServed`) becomes `wt-model served` (`cmd/wt-model/…` `runServed`; find the file with `git grep -n 'func runServed' -- wt/cmd`). `wt stop <provider>` becomes `wt-model stop`.
- Modify: `wt/docs/internals/testing.md:23,38,84-85`:
  - The `probeInventory` stubs belong to `cmd/wt-model`'s `TestMain`.
  - Registry writes are `modeladmin` and `modeleditor`.
  - The verification list uses the `wt-model` names.
- Modify: `wt/CLAUDE.md`:
  - "Go module" package list (`:94`): replace `configeditor` with `sessioneditor,modeleditor,editorkit,modeview,modelmode,modelping,urlrule,cli,servestate`.
  - "Config (Go)" (`:102`): "the `wt config` Mode tab writes it through `modelmode` (and `wt-model setup` through `modelsetup`)".
  - "LiteLLM routing state" (`:111`): Mode tab; "host commands read the registry through `app.hostConfig()`" becomes "`wt-model`'s commands read the registry (`cmd/wt-model/host.go`); `wt` reads it only best-effort through `app.hostConfig()` (cleanup's protected paths, `stats`' fallback)".
  - "Registry" (`:118,127`): `wt-model litellm list`, `wt-model model init`.
  - `:122,159`: `wt-model model add|edit`.
  - "Lifecycle" (`:149`): "the model server's jobs, `wt-model stop <provider>`/`--all`". `:155` "`wt stop` still stops it" becomes "`wt-model stop`".
  - "Start/stop": the `cmd/wt-session/model_cmds.go` path stays only for `start`/`stop <model>`.
  - "Cloud sync" (`:218-224`): heading `wt-model cloud-sync`, files `cmd/wt-model/cloudsync*.go`, `wt-model model add`/`edit --source`.
  - "LiteLLM routes" (`:228`): heading `wt-model litellm`, `cmd/wt-model/litellm.go`; `set|status` stay in both binaries; "`restart` is what `bin/update/litellm` runs" names `wt-model litellm restart`.
  - "wt-model": add "`wt-model` holds every host-admin command and the TUI (`setup`, bare `model`); `wt`'s old names are pointers (`cmd/wt-session/moved.go`, `TestMovedCommandsPointAtWtModel`)". Replace "`wt-model` is the server, `wt model` the registry command" with "`wt-model model` is the registry command; `wt model list --server` is the session's catalog view".
  - "Verification commands" (`:325-331`): `wt-model model list [--json]`, `wt-model cloud-sync --dry-run`, `wt-model served|warm|model add|edit|rm|init|litellm`.
  - The redirected-run block (`:337-343`): `"$D/wt-model" model list` built with `go build -o "$D/wt-model" ./cmd/wt-model`, and "`wt-model cloud-sync` still runs the real `ollama`".
- Modify: root `CLAUDE.md`:
  - `:5`: "Model server setup: `wt-model setup` (local) or the Mode tab of `wt config` (native/remote)".
  - `:23`: "from `wt-model model list --json`".
  - `:25`: `wt-model cloud-sync …`; `:31`: `wt-model stop <provider>|--all` is the host stop.
  - `:34`: "the plain `wt model list` stays the host registry listing" becomes "`wt-model model list` is the host registry listing modeltiers reads".
  - Architecture `wt/` bullet: "Writes `registry.toml` only through `config.UpdateRegistry` (`wt-model model init|add|edit|rm`, the Models tab of `wt-model model`, `wt-model cloud-sync`)", "all LiteLLM management (`wt-model litellm sync|list|providers|restart`, `set|status` in both binaries)".
  - The `wt-model` bullet lists the admin commands, `setup` and the `serve.json` record.
  - rec/sushi bullets: `wt-model model add`.
  - Key Gotchas: "Stop mechanisms per backend" uses `wt-model stop omlx`/`--all`/`rec`/`sushi`; `wt stop <model>` stays for one model. "Routing is derived" says "is `wt-model litellm list` (a host command)".
  - `provider stop` line: unchanged unless it names an old command.

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Run the Task 75 sweep restricted to `-- wt/docs/internals CLAUDE.md wt/CLAUDE.md`.
- [ ] **Step 2: Run it and see it fail.** It lists hits in every named file.
- [ ] **Step 3: Implement.** Make the edits.
- [ ] **Step 4: Run the tests and expect PASS.**
  - The restricted sweep prints nothing.
  - `make check-links && make lint-skills`.
  - `git grep -n 'configeditor' -- wt/docs CLAUDE.md wt/CLAUDE.md ':!wt/docs/superpowers'` → nothing.
- [ ] **Step 5: Commit.** `docs(wt): internals pages and both CLAUDE.md files describe the two binaries as built`. Files: the six internals pages, `CLAUDE.md`, `wt/CLAUDE.md`.

### Task 74: CHANGELOG, the split spec's command homes, and "As built"

**Files:**
- Modify: `wt/CHANGELOG.md`, under `## Unreleased`:
  - `### Changed`: one **Breaking:** bullet. It names every moved command old → new (the Task 75 table), the pointer line `wt: <old> moved to wt-model — run 'wt-model <new>'; nothing was changed` with exit 1, that `wt model list --server`, `wt stop <model>` and `wt litellm status|set` stay, and that `wt config` now has two tabs (Agents, Mode) with local set up by `wt-model setup`. If PRs 4a–4d added per-family bullets, fold them into this one.
  - `### Added`: `wt-model setup`, `wt-model model` (TUI), `wt-model` `served`/`warm`/`stop <provider>|--all`/`cloud-sync`/`model …`/`litellm sync|list|providers|restart`, and the `serve.json` runtime record.
  - The existing "A **Model server tab** in `wt config` (now three tabs)" bullet gets a trailing "(since split: the Mode tab of `wt config` and `wt-model setup`)".
- Modify: `docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md:77`. After the **Command homes.** paragraph, add `**Amended <date> (phase 4 stage 2, as built):**` and a table Command | Binary with these rows:
  - `model init|list|add|edit|rm`, bare `model` (Models tab), `setup` (Setup tab), `cloud-sync`, `litellm sync|list|providers|restart`, `served`, `warm`, `stop <provider>|--all`, `serve` → `wt-model`.
  - `litellm status|set` → both.
  - the launcher, `config` (Agents, Mode), `start`, `stop <model>`, `smoke`, `job`, `rotate`, `profile …`, `stats`, `model list --server`, the cleanup commands → `wt` (`wt-session`).

  Add one sentence: the paragraph's earlier `start`, `stop` and `litellm list` on the host, and `model list` read-only in the session, are superseded by this table.
- Modify: `docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md`: append `## As built`, modelled on `2026-11-17-wt-session-model-server-config-design.md:238`. Open with "Recorded after the build (<date>). Everything above stays as approved; this section lists where the shipped code differs from it and what was decided on the way."
  - `### PRs`: one line per PR (1a … 5b) with its number. Get the numbers with `gh pr list --state merged --search 'phase 4 stage 2 in:title' --json number,title --limit 30`.
  - `### Deviations and builder decisions`: one bullet per spine §15 pick 1–22 that the design text did not state:
    - the `modeview` and `cli` leaves, and `servestate` with `serve.json`;
    - the Setup tab offers local and native only (remote lives in `wt config`);
    - sessions are read through `GET /control/v1/catalog` `Row.Sessions`;
    - `wt-model stop <model-id>` is a usage error;
    - `spend.Row`/`Result` alias the `modelapi` types;
    - `WithSessions` lives in `cli`, and wt-session drops `WaitPendingRoutes`;
    - the binary ratchet is the shrinking `sessionBinaryAllow`, empty since 5a;
    - the 1c/3c size exceptions;
    - 4a and 4d split into 4a-i/ii/iii and 4d-i/ii;
    - the boundary scan now covers every wt-session file, with four allowed host reads;
    - the floors re-based to <N1>/<N2>/<N3>.
  - `### Left open`: #641, #642, #643, `issues.md` #6 (unchanged by this stage).

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** `grep -c '^## As built' docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md` → `0`. `grep -c 'Amended .*phase 4 stage 2' docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md` → `0`.
- [ ] **Step 2: Run it and see it fail.** Both print `0`.
- [ ] **Step 3: Implement.** Make the three edits; `<date>` is `date +%F` at commit time.
- [ ] **Step 4: Run the tests and expect PASS.** Both greps print `1`; `make check-links`.
- [ ] **Step 5: Commit.** `docs: changelog for the hard cut; amend the split spec's command homes; stage 2 as built`. Files: `wt/CHANGELOG.md`, both spec files.

### Task 75: the repo-wide old-name sweep reaches zero

This task fixes every hit the earlier tasks did not own: Go comments and strings under `wt/internal` and `wt/cmd`, skills (`.claude/skills`, `wt/.claude/skills`), `bin/`, `benchmarks/` (scripts and docs, not `results/`), `docs/reference`, `docs/contracts` (prose only), guides 00, 01, 03, 05, 07, 09–12, `wt/docs/wt-*.md`, `wt/docs/wt-agents/`, `llmbench`, `modeltiers` and `agent-utilities`.

**Mapping (old → new):**

| Old | New |
|---|---|
| `wt model init|list|add|edit|rm` | `wt-model model init|list|add|edit|rm` (`wt model list --server` unchanged) |
| bare `wt model` (the Models tab) | `wt-model model` |
| `wt cloud-sync` | `wt-model cloud-sync` |
| `wt litellm sync|list|providers|restart` | `wt-model litellm …` (`status|set` unchanged) |
| `wt served`, `wt warm` | `wt-model served`, `wt-model warm` |
| `wt stop <provider>`, `wt stop --all`, `wt stop omlx` (any provider name) | `wt-model stop …` (`wt stop <model-id>` unchanged) |
| "Model server tab" | "Mode tab" (native, remote, the local pointer) or "`wt-model setup`" / "the Setup tab" (local machine setup), by context |
| "Models tab of `wt config`" | "Models tab of `wt-model model`" |
| `configeditor` | `sessioneditor` (Agents, Mode) or `modeleditor` (Models, Setup), by context |
| `wt config` "three tabs" | "two tabs (Agents, Mode)" |

**The sweep command** (from the repo root; the Task 71–73 restricted runs replace `-- .` with their files):
```bash
git grep -nE '(^|[^-[:alnum:]_/])wt (model( (init|list|add|edit|rm))?|cloud-sync|litellm (sync|list|providers|restart)|served|warm|stop (--all|<provider>|omlx|ollama|mtplx|rec|sushi|mlx_lm_server|llamacpp))([^/[:alnum:]_-]|$)|Model server tab|configeditor|(wt config|config editor)[^.]{0,60}(Models tab|three tabs)' \
  -- . ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!issues.md' ':!benchmarks/results' ':!wt/CHANGELOG.md' \
       ':!wt/cmd/wt-session/moved.go' ':!wt/cmd/wt-session/moved_test.go' \
  | grep -v 'wt model list --server' | grep -v '^wt/docs/wt-session.md:[0-9]*:| `wt '
```
The exclusions are dated or historical records (specs, plans, archive, `issues.md`, benchmark results, the CHANGELOG's history, the pointer table and its test), the session command `wt model list --server`, and the moved-commands table rows in `wt-session.md`.

**Files:** every file the sweep lists after Task 74. Each hit is fixed by the table. A test that pins a changed user-visible string changes with it in the same commit, and so does a `docs/contracts` fixture that quotes it (`job.sample.json` keeps `wt model list --server`). A hit that is a deliberate quotation of the old name, for example a removed-command test feeding `"cloud-sync"` through `wt`, is rewritten so it does not spell `wt <old>` in one string, or moved into `moved_test.go`. No new exclusion is added to the command.

**Interfaces:** none.

- [ ] **Step 1: Write the failing check.** Run the sweep command and save the hit list: `… > /private/tmp/claude-501/-Users-keith-github-ohanaverse-local-ai-setup/d909167d-4749-46b9-a55e-39439a049147/scratchpad/sweep-before.txt; wc -l <that file>`.
- [ ] **Step 2: Run it and see it fail.** The count is greater than 0. Group it by area: `cut -d: -f1 <file> | sed 's|/[^/]*$||' | sort | uniq -c`.
- [ ] **Step 3: Implement.** Fix each hit by the table, area by area. After Go edits, run `cd wt && gofmt -l . && go vet ./...`. After a user-visible string change, run the package that pins it: `git grep -ln '<old text>' -- 'wt/**/*_test.go'`, then `go test -count=1 ./<pkg> -run <Test>`.
- [ ] **Step 4: Run the tests and expect PASS.**
  - The sweep prints nothing (exit 1 from the final `grep -v`, empty output).
  - `make lint` (shellcheck, skills, links).
  - `cd wt && go build ./... && go vet ./... && go test -count=1 ./internal/... ./cmd/wt-model`, with `cmd/wt-session` run in the background (about 3.5 minutes).
  - `(cd modeltiers && uv run pytest -q) && (cd llmbench && uv run pytest -q)`.
- [ ] **Step 5: Commit.** `docs: no admin command is named under its old wt name anywhere (final sweep)`. Files: every file Step 3 touched. If more than about 40 files, split into `docs(sweep): …` and `chore(wt): old command names in comments and strings` (code plus its tests).

### Task 76: full gates, install check, PR 5b

**Files:** none, unless a gate fails. A fix then goes in its own commit `fix(<scope>): <what the gate found>`, with the owning task's files.

**Interfaces:** none.

- [ ] **Step 1: Run the gates in the background, with captured exits.** From the repo root:
  ```bash
  ( make test-all; echo "GATE_EXIT=$?" ) > /private/tmp/claude-501/-Users-keith-github-ohanaverse-local-ai-setup/d909167d-4749-46b9-a55e-39439a049147/scratchpad/gate-5b.log 2>&1
  ```
  Run it with `run_in_background`, with no inner `&`, and read `GATE_EXIT` from the log. Then run `bin/check-config-dirs-untouched`.
- [ ] **Step 2: Check the ratchet and boundary tests by name.** `cd wt && go test -count=1 ./internal/depcheck ./internal/removal` → PASS. `go test -count=1 ./cmd/wt-session -run 'TestSessionPathsNeverTouch|TestMovedCommandsPointAtWtModel'` → PASS.
- [ ] **Step 3: Check install in a scratch home.** It must not touch the real `~/.local/bin`:
  ```bash
  D=/private/tmp/claude-501/-Users-keith-github-ohanaverse-local-ai-setup/d909167d-4749-46b9-a55e-39439a049147/scratchpad/install-check
  rm -rf "$D" && mkdir -p "$D/.local/bin" && make -C wt install BINDIR="$D/.local/bin" && ls "$D/.local/bin"/{wt,wt-session,wt-model}
  "$D/.local/bin/wt" model add x y --family f; echo "exit=$?"   # one line: wt: model add moved to wt-model — run 'wt-model model add'; nothing was changed / exit=1
  "$D/.local/bin/wt-model" --help | grep -E '^  (serve|setup|model|cloud-sync|litellm|served|warm|stop) '   # eight commands listed
  HOME="$D" bin/update/wt --check | tail -1          # [update:wt] current — … (all three present)
  ```
  The `wt model add` line must not reach a real registry: run it with `WT_REGISTRY="$D/registry.toml" WT_LITELLM_CONFIG="$D/config.yaml"`. Afterwards `test ! -e "$D/registry.toml"` (the pointer wrote nothing).
- [ ] **Step 4: Run the sweep once more.** The Task 75 command prints nothing. `make check-links`.
- [ ] **Step 5: Open PR 5b.** Title: `docs: phase 4 stage 2 close-out (5b): docs sweep, install, command homes, as built`. Write the body with `--body-file`: a summary of Task 70–75, the gate log's `GATE_EXIT=0`, the install-check output, the empty sweep, an "After merge" step to reinstall the globally installed skills the stage changed (`make install-skill-claude`, plus `install-skill-pi`/`install-skill-codex` where used: `wt-repo-cleanup` names `wt-model model init` since 4a-ii), and `Refs` to the phase-4 issue(s); no closing keyword. Merge with `gh pr merge --squash --admin` once CI is green, only when the user says so.

---

## Self-review

### Spec coverage

| Design requirement / section | Task(s) | Note |
|---|---|---|
| Goal 1: `wt-session` has no admin command and links no admin package; ratchet covers `cmd/wt-session` | 36–38, 42, 45, 49–51, 60 (commands leave); 63 (binary ratchet); 64–66 (spend, lifecycle cut, allow-list emptied); 67–68 (boundary scan, leftovers) | `config` and `provider` stay linked as leaf libraries [call] |
| Goal 2: `wt-model` has every admin command, same flags/output/exit codes, plus `setup` and a TUI | 25 (`model` TUI), 28 (`setup`), 37–38 (`model …`), 42 (`served`, `warm`), 45 (`cloud-sync`), 49–51 (`litellm …`), 59 (`stop`) | exit codes and JSON pinned by the moved tests and fixtures |
| Goal 3: `wt config` = Agents + Mode; local stores defaults and points at `wt-model setup` | 21, 29, 30, 31 | offer to run `wt-model setup` when on PATH: 30, 31 |
| Goal 4: one definition of mode/url/token rules; url `@` rule once | 1 (urlrule), 2–3 (modelping), 4–12 (modelmode), 22 (modeview) | `TestProxyURLRuleIsModelclientsRule` deleted in 1 |
| Goal 5: old names point and exit 1; callers move in the same change set | 23, 36 (stub family + table test); callers per family: 39–41, 43–44, 46–48, 51–53, 60–62 | final sweep 75 |
| Non-goals (no API, gateway, registry schema, lifecycle, tiers, modeltiers contract change) | none needed | 56 reads the existing `GET /control/v1/catalog` (`Row.Sessions`), no API change; modeltiers keeps the same fixture (41) |
| Anti-goal: two copies of the rules or a session-side launchd/LiteLLM copy | 1, 4–12, 19–20, 22, 30 | 30's Mode tab has no Control, LiteLLM, listen or master fields |
| Anti-goal: pointer exit 0 / old name still works | 36 (table test, panic on a live name) | |
| Anti-goal: `wt-model` linking the session half; `wt-session` linking admin packages | depcheck edits in 1, 2, 4, 13, 22, 24, 29, 31, 33, 55, 63, 66 | `TestModelServerBinaryStaysOffTheSessionPackages` runs in 25, 28, 58, 59 |
| Anti-goal: half-configured state from two editors disagreeing on `[model-server]` | 11 (both editors' config step is `modelmode.ApplyStep`), 27 (Setup: local/native only), 30 (Mode: native/remote/local pointer) | covered: `TestModeTabRemoteThenSetupTabLocalAgree` (27) drives the Mode tab's apply and the Setup tab's observe and plan against one `config.toml`; the shared Apply step and the "remote is set in `wt config`" line (27) are the other guards |
| Packages and import graph table | 1–3, 4–12, 13–17, 22, 24, 27, 29–31, 33, 55 | `modeview` and `cli` are builder additions (spine §15.8, §15.13) |
| Ratchet changes and the `cmd/wt-session` test | 63 (allow-list form), 64–66 | the design's "list that starts with the cut-loose packages and adds spend/lifecycle" is implemented as the shrinking `sessionBinaryAllow` (spine §15.19); recorded in As built (74) |
| `modelmode`: what moves, what stays, how modelsetup extends it | 4–6 (rules, words, steps, seams), 7 (State embeds, `Tools`), 8 (Observe + `LocalFallback`), 9 (Desired/Plan embed, `Admin`), 10 (BuildPlan, local pointer, hook), 11 (Apply/ApplyStep), 12 (url-rule tests split) | |
| Ping seam returning a classified result; `modelping` one ping | 3, 6 | `DefaultEnv` uses `modelping.Checked` directly (spine §15.3) |
| Local pointer plan keeps stored values only when the stored mode is already local; "LaunchAgent untouched" note | 10, 30 | `TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal` (10), `TestModeTabRemoteToLocalNeverKeepsTheRemoteURL` (30) |
| Secret-scoping stated once per package | 10 | |
| `TestNoPieceOfAnAtURLReachesAnyOutput` split | 12 | |
| `sessioneditor` (Agents + Mode, `LoadSession`, `ValidateSession`, synthesized agents never written) | 21, 29, 30, 31 | |
| `modeleditor` (Models + Setup), `wt-model model` / `setup`, bare `wt-model` stays help | 24, 25, 27, 28 | |
| `syncInputs` and helpers move to an admin package in stage 3 | 19, 20 | into `internal/litellm` |
| `editorkit` | 13–18 | |
| Command homes after the cut (incl. `wt model list --server`, `stop <model>` stay) | 37 (`--server` kept), 60 (`stop <model>` kept), 49–51 (`status|set` kept) | |
| Serve runtime record (0600, no token, removed on clean exit, ping-verified, allow-list entry, ships before the stop move) | 54–57 (4d-i), consumed by 58–59 (4d-ii) | the design's "read sessions through `sessionreg` directly" sentence is superseded by the user's option A (the record), which the plan follows |
| Pointer family: one constructor, one table test, vacuity floors re-based, word guard stays | 23, 36, 69 | |
| Callers: modeltiers, llmbench, `bin/update/litellm`, mtplx matrix, `bin/update/rec|sushi`, `bin/mlx-quantize`, six skills, Go strings, ~70 docs, both CLAUDE.md, fixtures' readers | 41, 43, 51, 41 + 60, 60, 41, 41/44/47/53, 39/46/52/61, 26/32/40–41/44/48/53/62/70–75, 37 (`model_list_contract_test.go` moves) | `job.sample.json` unchanged |
| Shared helpers in `cmd/wt-session` (shared CLI leaf; `hostConfig` stays for cleanup and stats; `boundary_test.go` list grows) | 33–35, 65, 67, 68 | |
| Staging 1–5 | PR table in Execution notes | 3, 4a and 4d split further (spine §13) |
| What users see; install unchanged (`make install` builds all three) | 70, 76; `bin/update/wt` knows wt-model from 41 (4a-ii), the first PR whose callers need it; `single-p` follows the package moves in 12 and 31 | |
| Risks: Observe cut, goldens split, test helpers not shareable, hand-written local, hard cuts | 8 + baseline check, 10, 8/12 (fakes copied), Review Focus #4, 36 | |
| Close-out: command-homes amendment, As built | 74 | |

Gaps flagged: the two-editor agreement test (partial, above); no task needed for the non-goals. Nothing in the design is unassigned.

### Type consistency

Names used across tasks were checked against the spine (Appendix A):
- `host` in `cmd/wt-model` (Task 25) now carries `hostCfg`, `hostLoadErr`, `hostCfgErr`, `hostSet`, `hostConfig()`, `hostConfigErr()`, the names Tasks 37, 38, 42, 45, 50 and 59 consume; one `h` is built in `rootCmd` and passed to every command.
- `movedError` is created once (Task 23); Task 36 adds `movedCommand`, `movedCommands`, `movedStub`, `addMovedStubs` beside it (spine §10).
- `adminScreens` is introduced by Task 13 and appended to in Tasks 24 (modeleditor), 29 and 31 (sessioneditor in, configeditor out), matching spine §12.
- `modeLeaves` (Task 1) is the list `TestModeLeavesStayOffTheClient` walks; Tasks 2, 4, 13, 22, 33 append to it.
- Package-var seams in `cmd/wt-model` (`probeInventory`, `seedEnv`, `ollamaCaps`, `syncRoutesAfterWrite` with its named `realSyncRoutesAfterWrite`, `openTTY`, `stdinTTY`, `confirmRemove`/`promptStop`, `withRegistryHint`) are declared once: Task 25's `seams.go` first, the git-mv'd files' duplicates deleted in Tasks 37/38/45/59, `stdinTTY` switched to `cli.IsStdinTTY` in Task 35.
- `rootCmd(mk depsFactory)` becomes `rootCmd(mk depsFactory, rt runtime)` in Task 57; tests written before it (Tasks 25, 28, 45, 49–51) call `rootCmd(fakeDeps)` and are updated there ("every `rootCmd(` call").
- `ServerKind` is `modelping.Kind`, aliased by `modelmode` and `modelsetup` (Tasks 6–7); `Secret`, `Step`, `Result`, `RefusalError` are `modelmode` types aliased in `modelsetup` (Task 5).
- `litellm.SyncInputs`, `DesiredRouteIDsFor`, `Report`, `ReportSyncPlan`, `RunSync`, `SyncAfterWrite`, `SyncAndWarn`, `ErrIDFailed`, `Probe` (Tasks 19–20) are the names Tasks 25, 28, 38, 50 consume.
- `sessioneditor.Options{StartTab, Mode, LookPath, UserHome}` / `Result{RunModelSetup, ModelSetup}` and `modeleditor.Options{StartTab, Models, Setup, UserHome}` / `Result{RegistryChanged}` match spine §6–§7.
- `servestate.Record/Store/Default/Write/Read/Remove/Sessions` and `config.ModelHome` (Tasks 54–56) match spine §11; `hostSessionReader(ctx, w, store)` (Task 58) is a new function in `cmd/wt-model`, distinct from the session one Task 60 deletes.
- `cli.WithSessions` is added in Task 65 (spine §8 lists it for 5a); `cli.VisibleID` is an addition the spine did not list (Task 34).
- `spend.Row`/`Result` become aliases of `modelapi.SpendRow`/`SpendReport` (Task 64).

### Proportion check

- 76 tasks across 16 PRs: 1a 3, 1b 3, 1c 6, 2 6, 3a 4, 3b 4, 3c 6, 4a-i 4, 4a-ii 5, 4a-iii 3, 4b 4, 4c 5, 4d-i 4, 4d-ii 6, 5a 6, 5b 7.
- Code-moving PRs carry most of the risk and most of the tests; documentation is ~18 tasks (26, 32, 40–41, 44, 47–48, 53, 62, 70–76 in part), which matches roughly 70 docs, six skills and two CLAUDE.md files that name moved commands.
- Two PRs exceed the ~1,500-line review bar (1c ~2,400; 3c ~1,800 + ~4,000 moved) because a half-cut `Observe` or a `wt config` with no local path would break the tree; both are reviewed commit by commit.
- Several tasks are mechanical moves whose "failing test" is the build failure of the moved test file; that is intentional (the moved suite is the net) and each still names its run command.


## Appendix A: interface spine (binding for every task; "spine §N" refers here)

Binding for every stage planner. Source: `docs/superpowers/specs/2026-11-18-wt-phase4-stage2-design.md` (the design). Paths are under `wt/` unless they start with `docs/`, `modeltiers/`, `llmbench/`, `bin/`, `benchmarks/`. "from X:N" = moved or adapted from that file and line on `origin/main` (f60ad48a). All module paths are `github.com/ohanaverse/local-ai-setup/wt/internal/<pkg>`. A name marked (alias) keeps compiling at every old call site; a name marked (new) has no predecessor.

Verified against the tree (`go list -deps`, `go list -f '{{.Imports}}'`): `modelapi` imports nothing internal; `config` imports `provider`, `tokenfile`, `tomlw`; `modelclient` imports `modelapi`, `tokenfile`; `litellm` → `config`, `localmodels`, `provider`; `localmodels` → `catalog`, `config`, `provider`; `cmd/wt-model` → `config`, `exitcode`, `lifecycle`, `modelserver`; `configeditor` → `agents`, `config`, `localmodels`, `modeladmin`, `modelclient`, `modelsetup`, `themes`, `tuilayout`. Session files of `cmd/wt-session` that import an admin package today: `answerer.go`, `main.go`, `start.go` (lifecycle), `stats_usage.go` (spend), `models.go`, `commands_config.go`, `model.go` (configeditor, modelsetup, litellm); every other hit is a host-command file.

### 1. `internal/urlrule` (new, stdlib only) — PR 1a

```go
const Hidden = "(hidden: the url contains an @)"   // from modelclient/urlcreds.go:17 (HiddenURL)
func HasUserinfo(raw string) bool                   // from modelclient/urlcreds.go:22
func Shown(raw string) string                       // from modelsetup/urlcreds.go:52 (shownURL), config/proxyurl.go:32
```
- `modelclient`: `const HiddenURL = urlrule.Hidden`; `func HasUserinfo(raw string) bool { return urlrule.HasUserinfo(raw) }` (urlcreds.go keeps `CredentialsInURLError` only until 1a moves it, below).
- `config/proxyurl.go` keeps only `ErrProxyURLHasAt` and three one-line delegations (no second copy of the rule): `const ProxyURLHidden = urlrule.Hidden`, `func ProxyURLHasAt(raw string) bool { return urlrule.HasUserinfo(raw) }`, `func ShownProxyURL(raw string) string { return urlrule.Shown(raw) }`. Callers (`cmd/wt-model/serve.go`, `cmd/wt-session/litellm.go`, `config/proxyhome.go`, `lifecycle/profile_routes.go`, `litellm/restart.go`) are unchanged.
- Delete `TestProxyURLRuleIsModelclientsRule` and every `modelclient` import in `config/proxyurl_test.go` (package `config`, internal test). **Required**, not cosmetic: from PR 1b `modelclient → modelmode → config`, so that test file would be an import cycle.

### 2. `internal/modelping` (new; imports `modelapi`, `urlrule`, stdlib) — PR 1a

The one ping, its HTTP transport and its classification. `modelclient` aliases every type, so `errors.As(err, &*modelclient.SkewError)` keeps working (an alias is the same type).

```go
type Endpoint struct{ URL, Token string }                 // from modelclient/endpoint.go:13
func (e Endpoint) Origin() string                         // endpoint.go:18
func (e Endpoint) Gateway() (origin, token string)        // endpoint.go:21

const UnparsedAddr, UnbuildableRequest = …  // modelclient/urlcreds.go:38,42, texts unchanged
const ServerName, DefaultTimeout, DialTimeout, MaxResponse = "wt-model", 10*time.Second, 5*time.Second, 32<<20 // client.go:22-28
func Addr(rawURL string) string        // from (*modelclient.Client).addr, client.go:89
func Clean(s string) string            // from modelclient/clean.go:14 (clean)

// moved from modelclient/errors.go and urlcreds.go, texts byte for byte
type UnreachableError struct{ Addr, Reason string }; type UnauthorizedError struct{ Addr string }
type SkewError struct{ Addr string; Server, Client int }; type SchemaError struct{ Addr string; Server, Client int }
type NotModelServerError struct{ Addr, Why string }; type ResponseTooLargeError struct{ Addr string; Limit int }; type CredentialsInURLError struct{}

func NewHTTPClient() *http.Client      // from client.go:72-81 (no env proxy, no redirects, 5 s dial)
type Response struct{ Status int; Header http.Header; Body []byte }
// Send: client.go:121-178 (the addr refusals, timeout wording, size cap,
// transport errors); NOT the Date-offset bookkeeping, which stays in modelclient.
func Send(ctx context.Context, hc *http.Client, timeout time.Duration, ep Endpoint, method, rawurl string, body any) (*Response, error)
// CheckPing: client.go:256-274 (401 → *UnauthorizedError, non-200, JSON, server name, APIMajor, Schema).
func CheckPing(addr string, r *Response) (modelapi.Ping, error)
func Ping(ctx context.Context, ep Endpoint) (modelapi.Ping, error) // Send(NewHTTPClient(), DefaultTimeout, GET /control/v1/ping) + CheckPing
// GetJSON: Ping handshake, then GET path, non-2xx → typed error, decode into out. Used by the host stop's session read (§9).
func GetJSON(ctx context.Context, ep Endpoint, path string, out any) error

type Kind int                          // from modelsetup/observe.go:116 (ServerKind)
const (KindNone Kind = iota; KindUnreachable; KindUnauthorized; KindNotWtModel; KindSkew; KindSchema)
type Outcome struct{ Kind Kind; Message string; API, Schema int } // Message = err.Error(); API/Schema from Skew/SchemaError.Server
func Classify(err error) Outcome       // from modelsetup/observe.go:629-648 (pingServer's switch)
// Checked is the default modelmode Net.Ping (design: "a seam returning an already-classified result").
func Checked(ctx context.Context, ep Endpoint) (modelapi.Ping, Kind, string, error)
```
- `modelclient/endpoint.go`: `type Endpoint = modelping.Endpoint`. `errors.go`: `type UnreachableError = modelping.UnreachableError` (and Unauthorized, Skew, Schema, NotModelServer, ResponseTooLarge); `urlcreds.go`: `type CredentialsInURLError = modelping.CredentialsInURLError`. `UnknownModelError`, `GatewayDownError`, `IsCode`, `NotConfiguredError` stay in modelclient. `clean.go`: `func clean(s string) string { return modelping.Clean(s) }`, `CleanForTest` stays (held by `modelserver/client_e2e_test.go:95`). `(*Client).send` = `modelping.Send(...)` + Date offset; `(*Client).Ping` keeps its once-per-client cache and calls `modelping.CheckPing(c.addr(), r)`; `c.addr()` = `modelping.Addr(c.ep.URL)`.

### 3. `internal/modelmode` (new; imports `config`, `modelapi`, `modelping`, `urlrule`, stdlib) — PR 1b (types, env) and 1c (observe, plan, apply)

### 3.1 Mode rules (from `modelclient/mode.go`, whole file; modelclient keeps aliases)
```go
type Mode string; const (ModeNative Mode = "native"; ModeLocal = "local"; ModeRemote = "remote")
func ParseMode(s string) (Mode, bool)
type ModeSource int; const (SourceNone ModeSource = iota; SourceEnvMode; SourceEnvEndpoint; SourceStored; SourceInferred)
type Resolution struct{ Mode Mode; Source ModeSource; Problem string }
func ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution
type ModeUnsetError struct{}; type NativeModeError struct{ Source ModeSource }; type ModeProblemError struct{ Problem string }
func IsModeUnset(err error) bool; func IsNativeMode(err error) bool
```
modelclient/mode.go becomes aliases: `type Mode = modelmode.Mode`, the consts re-declared as `= modelmode.X`, `type ModeSource/Resolution/ModeUnsetError/NativeModeError/ModeProblemError = modelmode.…`, `func ParseMode`/`ResolveMode`/`IsModeUnset`/`IsNativeMode` one-line wrappers. `NoServer` stays in modelclient (it needs `NoServerConfigured`).

### 3.2 Secrets, refusals, url words (PR 1b)
```go
type Secret string                       // modelsetup/plan.go:23-47, with String/GoString/MarshalJSON/MarshalText/Reveal
type RefusalError struct{ Reasons []string } // plan.go:169
func Reason(fact, wayOut string) string  // plan.go:175 (reason)
func CredentialsReason(what string) string // plan.go:271
const NotCheckedAtURL = "not checked: the url contains an @; retype it without credentials" // urlcreds.go:27
const CheckHidden = "the check failed; the url contains an @ so the error text is hidden"    // urlcreds.go:41
func ShownURL(raw string) string { return urlrule.Shown(raw) }
func RedactCheck(msg string, urls ...string) string // urlcreds.go:66
const DefaultListen = "127.0.0.1:8787"             // env.go:36
func DefaultLocalTokenPath(configHome string) string // observe.go:549
func RemoteTokenPath(configHome string) string       // observe.go:555
```
modelsetup keeps `notCheckedPairAtURL`, `pairErrHidden` (pair rule) and aliases: `type Secret = modelmode.Secret`, `type RefusalError = modelmode.RefusalError`, `const DefaultListen = modelmode.DefaultListen`, wrappers for the two path funcs; `hasUserinfo/shownURL/redactCheck/hiddenURL` become the modelmode/urlrule names.

### 3.3 Steps and results (PR 1b)
```go
type StepKind string; type StepAction string
const (StepToken StepKind = "token"; StepConfig StepKind = "config")              // plan.go:91,98
const (ActionTightenToken StepAction = "tighten-token"; ActionWritePastedToken = "write-pasted-token"; ActionReplacePastedToken = "replace-pasted-token")
type Step struct{ Kind StepKind; Path, Detail string; Action StepAction; Value string } // plan.go:144
type Result struct{ Done []Step; Failed *Step; Err error; Warnings, Notes []string; Recovery string } // apply.go:47
func StepLabel(k StepKind) (string, bool) // the two labels of apply.go:126; false for any other kind
```
modelsetup: `type Step = modelmode.Step`, `StepKind`, `StepAction`, `Result` aliases; `StepToken`/`StepConfig` and the three actions re-exported `= modelmode.X`; its own kinds (`StepPair`, `StepPlist`, `StepBootout`, `StepBootstrap`, `StepKickstart`, `StepWait`, `StepRemovePlist`, `StepLiteLLMPlist`, `StepBackup`, `StepMasterKey`, `StepChmod`, `StepKickstartLiteLLM`) and actions (`ActionGenerateToken`, `ActionPairKey*`, `ActionWaitLiteLLM`) stay in plan.go, typed `modelmode.StepKind`/`StepAction`. Its `stepLabel` handles its kinds and falls back to `modelmode.StepLabel`.

### 3.4 Seams and the real machine (PR 1b; from `modelsetup/env.go`)
```go
type FS interface { ReadFile(path string) ([]byte, error); Stat(path string) (fs.FileInfo, error); Lstat(path string) (fs.FileInfo, error)
	WriteAtomic(path string, data []byte, perm os.FileMode) error; Chmod(path string, perm os.FileMode) error } // env.go:40 minus Remove, Backup
type ConfigStore interface { Read() config.ModelServerFile
	Update(change func(config.ModelServerState) (config.ModelServerState, error)) error } // env.go:74 minus Pair, SetPair
type Net interface { Ping(ctx context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error) }
type Host interface { Getenv(string) string; Home() (string, error) }
type Env struct{ FS FS; Config ConfigStore; Net Net; Host Host }
func DefaultEnv() Env // RealFS{}, RealConfig{}, RealNet{}, RealHost{}; RealNet.Ping = modelping.Checked

func GuardUnderTest(paths ...string) error                         // env.go:148
type RealFS struct{ Resolve func(string) (string, error) }         // env.go:171 (realFS; `resolve` exported)
func (x RealFS) GuardAncestorUnderTest(p string) error              // env.go:189
// ReadFile, Stat, Lstat, Chmod, WriteAtomic: env.go:204-254
type RealConfig struct{}  // Read, Update: env.go:402-405
type RealNet struct{}     // Ping
type RealHost struct{}    // Getenv, Home (with the test-home refusal, env.go:500-518)
func DanglingLink(f FS, p string) bool                             // observe.go:577 (takes FS, not Env)
```
modelsetup widens (env.go): `type FS interface{ modelmode.FS; Remove(string) error; Backup(path, dir string) (string, bool, error) }`; `type ConfigStore interface{ modelmode.ConfigStore; Pair() (config.ProxyPair, error); SetPair(url, key *string) error }`; `type Net interface{ modelmode.Net; Get(...); ListenerPID(...); ProcessStart(...) }`; `type Host interface{ modelmode.Host; Executable(); LookPath(string); Now(); Sleep(ctx, d); RandRead([]byte) error; GOOS() string; UID() int }`. `Env` keeps its fields (FS, Launchd, Config, Net, Host, LiteLLM, DesiredRoutes) with the widened types, plus `func (e Env) Mode() modelmode.Env { return modelmode.Env{FS: e.FS, Config: e.Config, Net: e.Net, Host: e.Host} }`. Real impls embed: `realFS{modelmode.RealFS}` (+Remove, Backup — `Remove` stays in `modelsetup/env.go`, so the deletion-scan allow-list entry and its count of 1 are unchanged), `realConfig{modelmode.RealConfig}`, `realNet{modelmode.RealNet}`, `realHost{modelmode.RealHost}`. Fakes (`modelsetup/fakes_test.go:350`): `Ping` returns `p, o.Kind, o.Message, err` with `o := modelping.Classify(err)`, so the typed errors the tests build keep working.

### 3.5 State, Observe (PR 1c; from `modelsetup/observe.go`)
```go
type ServerKind = modelping.Kind  // + const KindNone = modelping.KindNone … KindSchema
type ConfigState struct{ Path string; Exists bool; ParseErr, TableErr, RestErr string; Table config.ModelServerState; Problems []string; URLHadCredentials bool } // :67
type TokenState struct{ Path string; Exists, Loose, Empty, Dangling bool; Err string }                                                                // :79
type ServerState struct{ URL string; Checked, OK bool; Kind ServerKind; Message string; API, Schema int }                                            // :136
type HostState struct{ Home, ConfigHome string }   // the session half of :149; the rest is modelsetup's Tools
type State struct{ Resolution Resolution; EnvOverride string; Config ConfigState; Token TokenState; TokenFiles []TokenState; Server ServerState; Host HostState }
type LocalFallback struct{ TokenFile, Listen string } // (new) the admin side fills it from the agent plist
type ObserveOptions struct{ TokenPaths []string; CheckURL string; CheckToken Secret; CheckTokenFile string; Local LocalFallback } // observe.go:32 session half; Local zero in wt-session
func Observe(ctx context.Context, env Env, opts ObserveOptions) State
func PingServer(ctx context.Context, env Env, ep modelping.Endpoint) ServerState // observe.go:622
func ServerFailure(s ServerState) string                                          // plan.go:734
func TokenAt(st State, path string) (TokenState, bool)                             // plan.go:696
func ExpandIn(path, home string) string                                            // plan.go:713
```
`Observe` body = observe.go:257-299 (config, resolution, env names), :302-309 (home, ConfigHome only), :347-389 (token and TokenFiles; `st.Agent.TokenFile` → `opts.Local.TokenFile`), :484-523 (server; `st.Agent.Listen` → `opts.Local.Listen`, else `DefaultListen`). The three local fallbacks fire only when `Resolution.Mode == ModeLocal`, exactly as today; `Local.TokenFile` stays third in `TokenFiles` (default local, remote, agent, `TokenPaths`).

modelsetup (observe.go):
```go
type ToolsState struct{ Binary, BinaryErr, Path string; UID int } // was HostState's admin half; st.Host.Binary/Path/UID → st.Tools.*
type State struct{ modelmode.State /* promoted */; Platform Platform; Pair PairState; Agent AgentState; Listener ListenerState
	Tools ToolsState; LiteLLMPlist LiteLLMPlistState; LiteLLMInstall LiteLLMInstallState; LiteLLM LiteLLMState }
type ObserveOptions struct{ modelmode.ObserveOptions; ListenPort int; SkipLiteLLMProbe, ProbeLiteLLMWhenLocal bool }
type ServerKind = modelmode.ServerKind; type ConfigState = modelmode.ConfigState; type TokenState = modelmode.TokenState; type ServerState = modelmode.ServerState (+ Kind* consts)
```
`modelsetup.Observe` order: platform; `Host.Home()`; agent plist + `Launchd.Print`; `o := opts.ObserveOptions; o.Local = LocalFallback{st.Agent.TokenFile, st.Agent.Listen}`; `st.State = modelmode.Observe(ctx, env.Mode(), o)`; tools; pair; LiteLLM plist, install, listener; `CheckLiteLLM` in local mode. The ping now runs before the listener/LiteLLM probes (a call-order change a recording fake may see).

### 3.6 Plan (PR 1c; from `modelsetup/plan.go`)
```go
type Desired struct{ Mode Mode; URL, TokenFile string; PastedToken Secret; SaveUnchecked bool } // plan.go:54, session half
type Plan struct{ Want Desired; Steps []Step; Notes []string }
// Preflight: plan.go:188-215 (ParseErr, TableErr, ParseMode, PastedToken cleared unless remote).
func Preflight(st State, want Desired) (Desired, error)
// Postflight: plan.go:236-240 (RestErr when the plan has a StepConfig).
func Postflight(st State, p Plan) error
// LeavingLocal runs in native/remote plans after the token step, before the config step. Nil (wt-session):
// when the resolved mode is local, the note "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`".
type PlanHooks struct{ LeavingLocal func(p *Plan) }
func BuildPlan(st State, want Desired) (Plan, error)                     // = BuildPlanWith(st, want, PlanHooks{})
func BuildPlanWith(st State, want Desired, h PlanHooks) (Plan, error)    // native (plan.go:276), remote (:288), local pointer (new)
func (p *Plan) AddConfigStep(st State)  // plan.go:588 (config)
func (p *Plan) AddEnvNote(st State)     // plan.go:668
func (p Plan) Format() string           // plan.go:676
func (p Plan) WritesURL() bool          // plan.go:782
```
Local pointer plan (new, design §modelmode): one `StepConfig` with mode `local`; the stored `url` and `token_file` are kept (each empty one filled with its default) **only when the stored table's mode parses as `local`**; in every other case (stored mode remote, native or unset) the stored and the typed url and token file are ignored and `http://`+`DefaultListen` and `DefaultLocalTokenPath(st.Host.ConfigHome)` are written (a remote url must never become local's url); refuse (Reason) when `ConfigHome == ""` and a token path is needed; refuse a kept url holding an `@` (`CredentialsReason("the stored url")`; a url that is not kept is overwritten, never refused); note "run `wt-model setup` to set up wt-model on this machine". Unchanged config → the existing "keep [model-server]" note.

modelsetup (plan.go):
```go
type AdminWant struct{ Listen string; MasterKey Secret; RemoveAgent bool; LiteLLM LiteLLMAction } // (new) the admin half of Desired
type Desired struct{ modelmode.Desired; AdminWant }  // d.Mode, d.Listen … promoted
type Plan struct{ modelmode.Plan; Admin AdminWant; Agent *AgentSpec; LiteLLM *LiteLLMSpec } // p.Want.Listen → p.Admin.Listen, p.Want.MasterKey → p.Admin.MasterKey
func BuildPlan(st State, want Desired) (Plan, error)
```
`modelsetup.BuildPlan`: `modelmode.Preflight`; clear `MasterKey`/`LiteLLM` unless local (the secret rule stated once per package); local → its `planLocal`/`planTightenLiteLLM` (using `p.Plan.AddConfigStep(st.State)`, `AddEnvNote`); native/remote → `modelmode.BuildPlanWith(st.State, want.Desired, PlanHooks{LeavingLocal: func(p){ leaveAgent; keepLocalSetup }})` wrapped as `Plan{Plan: mp, Admin: want.AdminWant}` (Listen cleared); then `restartNote`. `Postflight` runs once per plan: inside `BuildPlanWith` for native/remote, in modelsetup after `planLocal` (not after `planTightenLiteLLM`, as today); the second `Preflight` inside `BuildPlanWith` is idempotent. Output text of every existing plan is unchanged.

### 3.7 Apply (PR 1c; from `modelsetup/apply.go`)
```go
func Apply(ctx context.Context, env Env, p Plan, progress func(string)) Result // apply.go:96 loop, no recovery
// ApplyStep runs the kinds modelmode owns: StepToken with ActionTightenToken/
// ActionWritePastedToken/ActionReplacePastedToken (apply.go:275-318) and
// StepConfig (apply.go:247-254). Any other kind or action: handled == false.
func ApplyStep(ctx context.Context, env Env, p Plan, s Step, r *Result) (handled bool, err error)
```
`modelsetup.applyStep` handles its kinds and `StepToken`+`ActionGenerateToken` (needs `RandRead`), then `modelmode.ApplyStep(ctx, env.Mode(), p.Plan, s, r)`; not handled → today's "step kind %q is not one Apply knows" refusal. `modelmode.Apply` refuses every admin kind with the same text: the session binary can never run an admin step. `WaitError.Kind` is `ServerKind`; `waitReady` calls `modelmode.PingServer`.

### 3.8 Tests and goldens of stage 1
- `modelsetup/testdata/plan_native.golden`, `plan_remote.golden` keep the admin text (LaunchAgent notes, bootout, remove-plist); new `modelmode/testdata/plan_native.golden`, `plan_remote.golden` (no LaunchAgent lines), `plan_local_pointer.golden` (+ the "untouched" note variant). `plan_local`, `plan_litellm_*`, `agent.golden.plist`, `litellm.min.golden.plist` stay.
- Move to `modelmode`: `TestSecretRedactsEveryPrintedForm`, `TestFormatIsDeterministic`, `TestPlanWritesURLIsData`, `TestBuildPlanRefusesAnUnknownMode`, `…ParseErrorBeforeAnyStep`, `…UnreadableTableBeforeAnyStep`, `…KeyElsewhereInTheFile`, `TestBuildPlanRemote*`, `TestPlanRefusesAPastedTokenOverADanglingLink`, `TestPlanReplacesAnUnreadableTokenWithAPastedOne`, `TestObserveTokenChoiceMirrorsTheSession`, `…ExpandsATildeInTheConfiguredTokenFile`, `…LoosenessOfTheTokenFile`, `…NamesEnvOverrides`, `…KeepsAnInlineConfigTokenOutOfState`, `…StripsUserinfoFromTheConfiguredURL`, `…RecordsThePingedURLWithoutUserinfo`, `…PingsTheEnteredEndpoint`, `…MissingConfigIsEmptyNotAnError`, `TestObserveClassifiesEachPingFailure` (→ `modelping.Classify` table test plus one Observe case), `TestApplyRemoteWritesThePastedTokenBeforeTheConfig`, `TestApplyWritesTheTokenFile`, `TestApplyTightensALooseTokenFile`, `TestApplyConfigRefusesAURLWithCredentials`, `TestBuildPlanRemoteRefusesAURLWithCredentials`, `TestRealFSRefusesPathsOutsideTheTempDirUnderTest`, `TestRealHostHomeRefusesTheDevelopersHomeUnderTest`, `TestRealWriteAtomic*`, `TestRealChmodGuardsTheResolvedPath`. Stay (admin): everything naming local, LaunchAgent, pair, LiteLLM, listener, backup, Control. Native/plan tests that assert a LaunchAgent note stay in modelsetup (`TestBuildPlanNativeOffersToRemoveAManagedAgent`, `TestBuildPlanLeavingLocalNeverRemovesTheTokenOrThePair`).
- `urlcreds_test.go`'s `TestNoPieceOfAnAtURLReachesAnyOutput` splits: remote/native/stored/entered/env surfaces → `modelmode/urlcreds_test.go`; the local `Format` surface stays. `pairurl_test.go` stays.
- `modelmode/fakes_test.go` (new): a trimmed copy of `modelsetup/fakes_test.go`'s fakeFS/fakeConfig/fakeNet/fakeHost (test files cannot cross packages; the copy is test-only, as the design allows). `modelmode/testmain_test.go`: `config.IsolateConfigHomeForTest`.
- `modelclient/mode_test.go` moves to `modelmode/mode_test.go`; `modelclient/urlcreds_test.go` stays (it tests the client's refusals through aliases).
- Struct literals of `modelsetup.State/Desired/Plan/ObserveOptions` in tests need the embedded field spelled (`State: modelmode.State{…}`): mechanical, all in `modelsetup/*_test.go` and `configeditor/server_*_test.go`.

### 4. `internal/editorkit` (new; imports `themes`, `tuilayout`, bubbletea, bubbles/textinput, lipgloss, stdlib) — PR 2
```go
func Scrub(s string) string                                   // configeditor/server_view.go:124 (+ userinfoRE :108, keyRE :113)
func TildePath(path, home string) string                      // models_tab.go:161, made pure (home passed; no package var)
func HomePath(path, stateHome, userHome string) string        // server_view.go:137 (falls back to TildePath(path, userHome))
func BreakPath(path string, width int) []string               // server_view.go:495
func WrapText(s string, width int) string                     // models_tab.go:152
func Flow(units []string, sep string, width int) []string     // models_tab.go:148
func WrapIndented(text string, width int) string              // server_view.go:728
func FitHints(width int, hints []string) string               // models_tab.go:398
func NewTextInput(value, placeholder string) textinput.Model  // agent_form.go:15
type FormField struct{ Label, Value string; Focus bool }      // form_view.go:11
func RenderFormFields(theme themes.Theme, fields []FormField) string // form_view.go:19
func TabBar(theme themes.Theme, names []string, current int) string  // tabs.go:140 (names passed)
type Tone int; const (ToneNone Tone = iota; ToneDim; ToneWarn; ToneBad) // server_view.go:90
const LabelW = 10                                             // server_view.go:86
type Line struct{ Label, Text string; Tone Tone }             // server_view.go:101 (serverLine)
func RenderLine(l Line, width int) []string                   // server_view.go:409
func Styles(theme themes.Theme) map[Tone]lipgloss.Style       // server_view.go:563-568
type StatusGroup struct{ Label string; Lines []string }       // server_view.go:534
func Groups(lines []Line, width int, styles map[Tone]lipgloss.Style) []StatusGroup // server_view.go:541
type Body struct{ Head []string; Groups []StatusGroup; Choice []string; Notes [][]string; Fields []string }
func FitBody(b Body, room int, dropOrder []string, dim lipgloss.Style) []string // server_view.go:596-660 (the drop loop)
func ChoiceLines(label string, opts []string, width int) []string              // server_view.go:446-460
func PromptView(bar, text, question string, hints []string, width, height int, dim lipgloss.Style) string // server_view.go:698-726
func PlanPromptView(bar, planText, question string, hints []string, width, height int, dim lipgloss.Style) string
type ResultScreen struct{ Recovery string; Notes []string; Text string; Top int } // server_litellm.go:223
func NewResultScreen(recovery string, notes []string, text string) *ResultScreen   // :232, scrubs; nil when nothing to show
func (r *ResultScreen) Lines(width int, warn, dim lipgloss.Style) []string         // :245
func (r *ResultScreen) Key(key string, width, height, barH int, warn, dim lipgloss.Style) (handled, done bool) // :288
func (r *ResultScreen) View(bar string, hints func(scrolls bool) []string, width, height int, warn, dim lipgloss.Style) string // :272,:305
func StatusBlock(status string, width int) string             // editor.go:139 (uncached; each shell caches)
```
`configeditor` calls these in PR 2 (no behaviour change); its `userHome` package var becomes a `home` field resolved once in `Run` from `Options.UserHome func() (string, error)` (default `os.UserHomeDir`; testmain sets it per test). Tests moving to `editorkit`: `TestBreakPathNeverSplitsASegmentThatFits`, `TestScrubTakesTheHostWithAnAtInTheQuery`, `TestScrubHidesKeysWithoutMangling`, `TestScrubTakesUserinfoThroughTheLastAt`; `TestTabBarShowsThreeTabs` stays and goes through `TabBar`.

### 5. `internal/modeview` (new; imports `modelmode`, `editorkit`, `urlrule`) — PR 3a
The mode/url/token/server words both editors show (no copy in each):
```go
func ModeSource(r modelmode.Resolution) string            // server_view.go:148
func EnvNames(st modelmode.State) []string                 // :163
func KindWord(k modelmode.ServerKind) string               // :259 (serverKindWord)
func ModeLine(st modelmode.State) editorkit.Line           // :358-369
func URLLine(st modelmode.State) editorkit.Line            // :370-374
func TokenLine(st modelmode.State, userHome string) editorkit.Line // :180
func ServerCheckLine(st modelmode.State) editorkit.Line    // :273 (+ missingForCheck :292)
func EnvLine(st modelmode.State) (editorkit.Line, bool)    // :386-388
func ProblemLines(st modelmode.State) []editorkit.Line     // :389-405
```
`configeditor/server_view.go` composes them in PR 3a (same order: mode, url, token, pair, agent, server, litellm, plist, env, problems).

### 6. `internal/sessioneditor` (new, from `configeditor`) — PR 3c
```go
type Tab int; const (TabAgents Tab = iota; TabMode)
var tabNames = []string{"Agents", "Mode"}
type ModeDeps struct{ Observe func(ctx context.Context, o modelmode.ObserveOptions) modelmode.State
	Plan func(st modelmode.State, want modelmode.Desired) (modelmode.Plan, error)
	Apply func(ctx context.Context, p modelmode.Plan, progress func(string)) modelmode.Result }
func ModeDepsFromEnv(env modelmode.Env) ModeDeps   // from tabs.go:60 (ServerDepsFromEnv), no Control
type Options struct{ StartTab Tab; Mode ModeDeps; LookPath func(string) (string, error) /* exec.LookPath */; UserHome func() (string, error) /* os.UserHomeDir */ }
type Result struct{ RunModelSetup bool /* accepted "run wt-model setup now" */; ModelSetup string /* path LookPath found */ }
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error) // from editor.go:458
```
Files: `editor.go` (shell: tab switching, held quit on mode writes, first-visit observe; copied from configeditor/editor.go and trimmed), `agents_tab.go`, `agent_form.go`, `delete.go`, `save.go` (git mv), `mode_tab.go` + `mode_view.go` (new, cut from server_tab.go/server_view.go: native/remote/local-pointer only; mode choice `native local remote`, all allowed on every platform; fields url, token file, paste for remote; check, plan, confirm `[y/N]`, apply; no Control keys, no LiteLLM menu, no listen/master fields, no remove-agent prompt). Save: `handleSave` validates with `cfg.ValidateSession()` (new, `config/config.go` next to `ValidateAll`:885: `default_tag` set; agents: non-empty, unique names, at least one supported provider, default provider in the list; `AllProviders` agents skipped; provider ids not checked). Writing is unchanged: `PatchSave` → `writeConfigFile`, which already drops `AllProviders` agents (`config/config.go:1388`); a test pins it. `Result.RunModelSetup`: cmd/wt-session runs `wt-model setup` as a child with the terminal after the editor exits.

### 7. `internal/modeleditor` (new, from `configeditor`) — PR 3b (Models), 3c (Setup)
```go
type Tab int; const (TabModels Tab = iota; TabSetup)
var tabNames = []string{"Models", "Setup"}
type ModelsDeps struct{ Load func() (*config.Config, error); Probe func(*config.Config) localmodels.Snapshot
	SeedEnv func() config.SeedEnv; Capabilities func(cfg *config.Config, name string) (map[string]any, error) } // tabs.go:97, unchanged
type SetupDeps struct{ Observe func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State
	Plan func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error)
	Apply func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result
	Control func(ctx context.Context, a modelsetup.ControlAction) error } // tabs.go:47 (ServerDeps)
func SetupDepsFromEnv(env modelsetup.Env) SetupDeps // the caller sets env.DesiredRoutes first
type Options struct{ StartTab Tab; Models ModelsDeps; Setup SetupDeps; UserHome func() (string, error) }
type Result struct{ RegistryChanged bool }          // tabs.go:127
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error)
```
Files: `models_tab.go`, `models_form.go` (git mv in 3b), `editor.go` (new shell: no save key, held quit on model or setup writes), `setup_tab.go`/`setup_view.go`/`setup_litellm.go` (git mv of server_tab/server_view/server_litellm in 3c; mode choice is `local native` — **remote is configured in `wt config`**, the Setup tab says so; leaving local keeps the remove-agent `[y/N]` prompt and bootout/remove-plist steps).

### 8. `internal/cli` (new, session-safe leaf; imports `exitcode`, `charmbracelet/x/term`, lipgloss, stdlib) — PR 4a-i
```go
var ErrNoTerminal = errors.New("there is no terminal to ask")     // cmd/wt-session/exitcode.go:50
const ExitApprovalMissing = 3                                       // exitcode.go:45
func ApprovalMissing(flag, state string) error                      // :55
func NoTerminalRefusal(err error, flag, state string) error         // :61
func Declined(state string) error                                   // :71
func ReadYesNo(r io.Reader, def bool) (bool, error)                 // launch.go:64 (askYesNoDefault)
// AskOnTTY: the body of promptStop (model_cmds.go:58) and promptCloudSync
// (cloudsync.go:107); open is the binary's own openTTY seam, noTTY the error
// to return without a terminal (ErrNoTerminal, or cloud-sync's --yes text).
func AskOnTTY(open func() (*os.File, error), question string, noTTY error) (bool, error)
func IsStdinTTY() bool                                              // helpers.go:128
func StdoutWidth() int                                              // stats_usage.go:244 (realStdoutWidth)
type PlainColumn struct{ Head string; Right bool }                  // plain_table.go:13
func PlainTableWidth(cols []PlainColumn, rows [][]string) (key, rest int) // :26
func RenderPlainTable(cols []PlainColumn, rows [][]string, width int) string // :67
func Dash(s string) string                                          // model_list.go:247
func WithSessions(s string, sessions int) string                    // lifecycle/message.go:69 — PR 5a
```
Each binary keeps its own package-var seams over these (`openTTY`, `stdinTTY = cli.IsStdinTTY`, `stdoutWidth = cli.StdoutWidth`): they move with their files, as the documented migrating exception. Stay in `cmd/wt-session`: `reportExit`, `agentExitStatus`, `writeResult`, `confirmYN`, `bothTerminals`, `isTerminalStream` (cleanup-only), `renderTable`/`borderStyle` (only `wt config theme` and `stats` use them), `mustGet*`, `hostConfig`/`loadHost`/`hostConfigErr` (cleanup's `protectedPaths`, `stats`), `configError` (= `config.ConfigError`; wt-model calls `config.ConfigError` directly). `exitcode` stays its own package.

### 9. Admin helpers that leave `cmd/wt-session` — PR 3a (into `internal/litellm`)
`litellm/syncinputs.go` (from cmd/wt-session/litellm.go:138-555):
```go
type Probe func(*config.Config) localmodels.Snapshot
func SyncInputs(cfg *config.Config, probe Probe) ([]config.Model, Options, []string) // :138 syncInputs
func DesiredRouteIDsFor(probe Probe) func(context.Context) ([]string, error)      // :164 desiredRouteIDs, for Env.DesiredRoutes
// unexported, moved as is: missingProviderWarnings, noProviderEntryReason, syncUntouchedAndWarnings,
// routedIDSet, desiredLocalIDs, desiredLocalEntries, desiredLocalModels, registryRowOf, untrustedFamilies, gapFamily, gapReason
```
`litellm/report.go` (from litellm.go:23-130, 173-196; model.go:34-53; model_write.go:243-253):
```go
type OutcomeJSON struct{…}; type ResultJSON struct{…}; type SyncPlanJSON struct{…} // litellmOutcomeJSON, litellmResultJSON, syncPlanJSON; tags unchanged (docs/contracts/litellm-cli.sample.json)
var ErrIDFailed = errors.New("one or more models could not be applied") // errLitellmIDFailed
func Report(out, errOut io.Writer, res Result, asJSON bool) error
func ReportSyncPlan(out, errOut io.Writer, plan SyncPlan, warnings []string, asJSON bool) error
func RunSync(out, errOut io.Writer, cfg *config.Config, probe Probe, asJSON, dryRun bool) error
func SyncAfterWrite(out, errOut io.Writer, probe Probe) string               // realSyncRoutesAfterWrite
func SyncAndWarn(out, errOut io.Writer, warnings []string, sync func(out, errOut io.Writer) string) error
```
Each binary keeps `probeInventory = localmodels.Inventory`, `func realSyncRoutesAfterWrite(out, errOut io.Writer) string { return litellm.SyncAfterWrite(out, errOut, probeInventory) }` and `var syncRoutesAfterWrite = realSyncRoutesAfterWrite` as seams (the named function is what the `realRouteSync` test helper assigns) — in `cmd/wt-session` only until their last caller moves: `desiredRouteIDs` goes in Task 31 (3c), `syncRoutesAfterWrite`/`seedEnv` in Task 45 (4b), `runLitellmSync` and `probeInventory` in Task 50 (4c); see "Deviations from the spine". `seedEnv`, `ollamaCaps` stay per-binary vars over `config.DefaultSeedEnv`/`modeladmin.OllamaCapabilities`.

### 10. Pointer stubs (in `cmd/wt-session/moved.go`, used by `cmd/wt-session` only) — PR 4a-i
```go
type movedCommand struct{ Old, New string } // Old "model add" (under wt), New "model add" (under wt-model)
var movedCommands = []movedCommand{}        // grows per PR: 4a-ii model init|add|edit|rm; 4a-iii served, warm; 4b cloud-sync; 4c litellm sync|list|providers|restart
func movedError(old, new string) error      // exitcode.New(1, errors.New("<old> moved to wt-model — run 'wt-model <new>'; nothing was changed"), false)
func movedStub(name string, m movedCommand) *cobra.Command // generalises litellm.go:725: Hidden, DisableFlagParsing, SilenceUsage, Annotations{removedCmdAnnotation: "moved to wt-model"}
func addMovedStubs(root *cobra.Command, table []movedCommand) // finds or adds the parent group by Old's first word
```
Main prints it as `wt: model add moved to wt-model — run 'wt-model model add'; nothing was changed`. Conditional pointers call `movedError` directly: bare `wt model` (PR 3b, New `model`), `wt model list` without `--server` (4a-ii), `wt stop <provider>|--all` (4d-ii). `removedLitellmCmd`'s text (#179 stubs) changes to `wt-model litellm sync` in 4c. One table test `TestMovedCommandsPointAtWtModel` (`moved_test.go`): each row and each conditional one prints one line, exits 1, leaves config.toml and registry.toml bytes unchanged; `TestNoWtCommandIsHidden` already skips the annotation.

### 11. Serve runtime record — PR 4d-i
Package `internal/servestate` (new, admin; imports `config`, `modelping`, `modelapi`, `modelmode` (for `GuardUnderTest`), `tokenfile`, stdlib). `config.ModelHome() string` (new, `config/proxyhome.go`, `<baseConfigHome>/wt-model` or ""; `ProxyHomePath` uses it).
```go
// JSON keys version (1), pid, listen (the bound address announce got), token_file (absolute path; never the token), started
type Record struct{ Version, PID int; Listen, TokenFile string; Started time.Time }
type Store struct{ Path string }           // per-call seam; tests pass t.TempDir()
func Default() Store                         // {config.ModelHome()+"/serve.json"}; Path "" → every call is a no-op/absent
func (s Store) Write(r Record) error         // dir 0700, file 0600 via config.WriteFileAtomic; GuardUnderTest in a test binary
func (s Store) Read() (Record, bool, error)  // absent → false, nil
func (s Store) Remove(pid int) error         // removes only a record whose PID is pid; the one os.Remove
func Sessions(ctx context.Context, r Record) (map[string]int, error) // token via tokenfile.Load(r.TokenFile); modelping.GetJSON(…, "/control/v1/catalog", &modelapi.Catalog); Row.Sessions > 0
```
`cmd/wt-model`: `rootCmd(mk depsFactory, rt runtime)` with `type runtime struct{ Record servestate.Store }` (tests pass a temp store); serve writes after announce, `defer rt.Record.Remove(os.Getpid())`. Allow-list `internal/removal/deletionscan_test.go`: `"internal/servestate/record.go": {1, "Store.Remove: removes wt-model serve's own runtime record (serve.json in the wt-model home) on clean shutdown, only when it names this pid"}`. Host stop (4d-ii): `hostSessionReader(ctx, w, store) func() map[string]int`: no record → nil silently; record + failed ping/read → `wt-model: note: could not read client sessions from the model server (<modelping.Clean(err)>) — models they use are stopped without asking`.

### 12. Import graph (final) and `depcheck_test.go` edits

| Package | May import (internal) | Lists |
|---|---|---|
| urlrule | — | sessionLibraries (1a) |
| modelping | modelapi, urlrule | sessionLibraries (1a) |
| modelmode | config, modelapi, modelping, urlrule | sessionLibraries (1b) |
| modelclient | modelapi, modelmode, modelping, tokenfile, urlrule | sessionLibraries, sessionBanned (as today) |
| modelsetup | config, litellm, modelapi, modelmode, modelping, urlrule | adminBanned |
| editorkit | themes, tuilayout | sessionLibraries (2) |
| modeview | editorkit, modelmode, urlrule | sessionLibraries (3a) |
| sessioneditor | agents, config, editorkit, modelmode, modeview, themes, tuilayout | sessionLibraries + sessionBanned (3c) |
| modeleditor | config, editorkit, localmodels, modeladmin, modelmode, modelsetup, modeview, themes, tuilayout | adminBanned (3b) |
| cli | exitcode | sessionLibraries (4a-i) |
| servestate | config, modelapi, modelmode, modelping, tokenfile | adminBanned (4d-i) |
| cmd/wt-session | session libraries, config, provider, exitcode, … ; no adminBanned entry | new binary ratchet (4d-ii, 5a) |
| cmd/wt-model | admin packages, cli, modeleditor, servestate; nothing in sessionBanned | as today |

Edits per PR: **1a** add urlrule, modelping to `sessionLibraries`; new `TestModeLeavesStayOffTheClient` (closures of `modelping`, `urlrule`, later `modelmode`, `editorkit`, `modeview`, `cli` must not contain `internal/modelclient`). **1b** add modelmode to `sessionLibraries` and that test. **2** add editorkit (both). **3a** add modeview (both). **3b** add modeleditor to `adminBanned`; `TestAdminScreensStayOffTheSessionScreens` iterates `adminScreens = []string{"internal/configeditor", "internal/modeleditor", "internal/editorkit"}`. **3c** delete configeditor from `adminBanned`, `sessionBanned`, `adminScreens`; add sessioneditor to `sessionLibraries`, `sessionBanned`, `adminScreens`. **4a-i** add cli (both lists). **4d-i** add servestate to `adminBanned`. **4d-ii** new `TestSessionBinaryStaysOffTheAdminPackages`: `ratchetProblems("cmd/wt-session", closure(t, "cmd/wt-session"), adminBanned, sessionBinaryAllow)` with `sessionBinaryAllow = []string{"internal/lifecycle", "internal/litellm", "internal/localmodels", "internal/spend"}` (lifecycle drags litellm and localmodels); shrink-only like `allowList`. **5a** empties it. Comment and CLAUDE.md "Import graph ratchet" paragraph updated in the same PRs.

### 13. PR sequence (entry → exit gate: `go build ./... && go vet ./... && make check && go test ./...` green, `go test ./internal/depcheck ./internal/removal` named)

| PR | Scope | Entry | Size (est. changed lines; moves via git mv count once) |
|---|---|---|---|
| 1a | urlrule, modelping; modelclient aliases + send/Ping delegate; config proxyurl delegation; delete `TestProxyURLRuleIsModelclientsRule`; modelping tests (Send/CheckPing/Classify moved from `modelclient/client_test.go` ping cases) | main | ~900 |
| 1b | modelmode mode rules (git mv mode.go + mode_test.go), Secret, RefusalError, url words, Step/Result, seams + Real*; modelsetup aliases and widened seams; fakes' Ping | 1a | ~1,100 |
| 1c | modelmode State/Observe/Preflight/BuildPlan(With)/pointer plan/Apply/ApplyStep; modelsetup embeds and delegates; goldens and tests split (§3.8) | 1b | ~2,400 — over the bar; cannot split further without a half-moved Observe (design Risk 1); review commit by commit: (i) types + embeds, (ii) Observe, (iii) plan, (iv) apply, (v) tests |
| 2 | editorkit; configeditor uses it; `Options.UserHome` | 1c | ~900 |
| 3a | `litellm/syncinputs.go`, `litellm/report.go`; cmd/wt-session calls them; `config.ValidateSession`; modeview; configeditor uses modeview | 2 | ~1,300 |
| 3b | modeleditor (Models tab, git mv); `wt-model model` (bare, TTY) with RegistryChanged → `litellm.SyncAndWarn`; cmd/wt-model `host.go` (`type host struct` from app.go:226-252), `themes.Load`; bare `wt model` → pointer; configeditor tabs Agents + Model server | 3a | ~1,200 + ~2,800 moved |
| 3c | Setup tab (git mv server_* → modeleditor), `wt-model setup`; sessioneditor (git mv agents files, new Mode tab); `wt config` → sessioneditor via `config.LoadSession()`; `ensureMode` opens `TabMode`, then runs `wt-model setup` when `RunModelSetup`; configeditor deleted | 3b | ~1,800 new/changed + ~4,000 moved — over the bar; split the review into commits (i) Setup move, (ii) Mode tab + tests, (iii) switch + delete |
| 4a-i | `internal/cli`; both binaries use it; `moved.go` + empty table + `TestMovedCommandsPointAtWtModel` (conditional rows: bare `wt model`) | 3c | ~700 |
| 4a-ii | `wt-model model init|list|add|edit|rm` (git mv model.go, model_list.go, model_write.go; `--server` stays in wt-session's `model list`); stubs; `modeltiers` (inventory.py, tests, conftest, guide 12), `benchmarks/mtplx-profile-matrix` (edit, list), `bin/mlx-quantize` hint, skills `wt-mlx-lm-quantization`, `wt-manage-slot-profiles`, `wt-adding-a-provider` | 4a-i | ~900 + ~2,800 moved |
| 4a-iii | `wt-model served`, `warm`; stubs; `llmbench/src/llmbench/wt_bridge.py` + tests; `wt-adding-a-benchmark-backend` skill | 4a-ii | ~600 |
| 4b | `wt-model cloud-sync` (git mv cloudsync*.go + 6 test files); stub; `agents/price_notice.go` text; `wt-cloud-sync` skill, `wt/docs/wt-cloud-sync.md`; deletion-scan entry renamed to `cmd/wt-model/cloudsync_catalog.go` | 4a-iii | ~500 + ~3,800 moved |
| 4c | `wt-model litellm sync|list|providers|restart` into `cmd/wt-model/litellm.go`; stubs; #179 stubs' text; `bin/update/litellm` (feature-detect `wt-model litellm restart`) | 4b | ~1,000 + ~2,000 moved (litellm_test.go is git mv'd to cmd/wt-model/litellm_sync_test.go and a small wt-session file recreated, so the move reads as a rename) |
| 4d-i | servestate; serve writes/removes the record; deletion allow-list; `config.ModelHome` | 4c | ~600 |
| 4d-ii | `wt-model stop <provider>|--all` (hostSessionReader over servestate); `wt stop <provider>|--all` → pointer; `benchmarks/mtplx-profile-matrix` `wt-model stop mtplx`; binary ratchet test | 4d-i | ~800 + ~1,700 moved |
| 5a | `stats` on `modelapi.SpendRow`/`SpendReport` (`spend.Row = modelapi.SpendRow`, `spend.Result = modelapi.SpendReport` aliases, same fields); `cli.WithSessions` (answerer); delete `lifecycle.WaitPendingRoutes` from wt-session `main.go:52` and the `start.go:35` seam (no route write left in the session); `sessionBinaryAllow` emptied; vacuity floors re-based | 4d-ii | ~400 |
| 5b | docs sweep (guides 02, 04, 06, 08, 13, 14, `wt-model-server.md`, `configuration.md`, both CLAUDE.md, `internals/packages.md`), command-homes table amendment, "As built" | 5a | docs only |

### 14. Tests, goldens and fixtures that move per PR (beyond §3.8)
- **1a**: ping/transport cases of `modelclient/client_test.go` (ping, skew, schema, redirect, too-large, unreachable wording) gain modelping twins; `modelclient/errors` texts stay pinned where they are. `internal/config/proxyurl_test.go` loses its modelclient import.
- **2**: four scrub/breakPath tests (§4) → `editorkit/*_test.go`.
- **3a**: tests in `cmd/wt-session/litellm_test.go` that call the moved helpers directly (desiredLocalIDs, syncUntouchedAndWarnings, missingProviderWarnings, gapReason, untrustedFamilies) → `internal/litellm/syncinputs_test.go`; those driving `runLitellmSync` stay until 4c. `server_view` line tests → `modeview` where they test only mode lines.
- **3b**: `configeditor/models_tab_test.go`, `models_form_test.go`, `models_pairing_test.go` → modeleditor; Models cases of `editor_test.go`, `quit_test.go`; `cmd/wt-session/commands_config_test.go` RegistryChanged/sync cases → `cmd/wt-model/model_tui_test.go`; `cmd/wt-session/model_test.go` bare-`wt model` cases → pointer rows.
- **3c**: `server_tab_test.go`, `server_aturl_test.go`, `server_polish_test.go`, `server_litellm_test.go` split: native/remote/at-url/mode-choice → `sessioneditor/mode_tab_test.go`; local/LiteLLM/Control/result screen → `modeleditor/setup_tab_test.go`; `agent_form_test.go`, `agents_tab_test.go`, `delete_test.go`, `save_test.go`, rest of `editor_test.go`/`quit_test.go`, `testmain_test.go` → sessioneditor (testmain copied to modeleditor); `mode_gate_test.go` (`TabServer` → `TabMode`, seam type `sessioneditor.Options/Result`); `commands_config_test.go` three-tab text.
- **4a-ii**: `model_test.go`, `model_list_test.go` (registry part), `model_list_contract_test.go` (still reads `../docs/contracts/model-list.sample.json`), `model_write_test.go` → `cmd/wt-model`; `TestHostCommandsStillLoadTheRegistry` loses those rows (they are not carried over: the moved `model list|add` tests load the registry themselves; `cmd/wt-model/host_test.go` is created in 4a-iii, Task 42, with the served/warm rows); `load_error_hint_test.go` model cases; testmain stubs (`ollamaCaps`, `seedEnv`, `probeInventory`, `syncRoutesAfterWrite`, `stdoutWidth`) copied to `cmd/wt-model/testmain_test.go`. `modeltiers/tests/conftest.py` keeps the same fixture.
- **4a-iii**: `TestServed*`, `TestRunServedSushiListsOnlyLoadedPacks`, `TestWarm*` (`model_cmds_test.go`) → `cmd/wt-model/served_test.go`.
- **4b**: `cloudsync_test.go`, `cloudsync_catalog_test.go`, `cloudsync_flownames_test.go`, `cloudsync_ollama_test.go`, `cloudsync_picker_test.go`, `cloudsync_removals_test.go` → `cmd/wt-model`.
- **4c**: `litellm_test.go` sync/list/providers/restart cases and `litellm_dup_test.go` → `cmd/wt-model/litellm_test.go` (status/set and `TestLitellmOnOffAreRetired` stay); both still read `docs/contracts/litellm-cli.sample.json`.
- **4d-ii**: `TestStopProvider*`, `TestStopAll*`, `TestStopBareOmlx*`, `TestStopInUse*`, `TestStopUnknownProvider*`, `TestStopInvalidArgErrors`, `TestStopFailurePropagates`, `TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow`, `TestHelpNamesSushiWhereItListsProviders` (stop half) → `cmd/wt-model/stop_test.go`; `stop_loading_test.go`, `stop_untrusted_test.go` → `cmd/wt-model`; `host_sessions_test.go` → rewritten as `cmd/wt-model/stop_sessions_test.go` + `servestate/record_test.go`; `host_commands_test.go` deleted (empty). `docs/contracts/job.sample.json` unchanged (`wt model list --server` stays).
- **5a**: `seen < 20` floors in `error_line_test.go:116` (`TestEveryRunnableCommandSilencesUsage`) and `model_cmds_test.go:815` (`TestNoHelpTextNamesModelman`) re-based to the counted commands of each binary; a twin floor in `cmd/wt-model`.

### 15. Decisions left to the builder, and the picks
1. **Where `Kind` lives**: `modelping.Kind`; `modelmode.ServerKind` and `modelsetup.ServerKind` are aliases, so both packages agree by construction.
2. **The typed ping errors move to `modelping`** (aliased in modelclient) rather than being rebuilt from an outcome: one definition of each text, and `errors.As` keeps working across both packages and the fakes.
3. **No ping injection needed in wt-session**: `modelmode.DefaultEnv` uses `modelping.Checked`; the Net seam stays for tests. Equivalent to the design's "inject one built on it", one less wiring point.
4. **`modelsetup.Plan` carries `Admin AdminWant`** beside the embedded `modelmode.Plan.Want`: embedding `Desired` alone cannot hold `Listen`/`MasterKey` without a shadowing `Want` field.
5. **Plan hooks**: `PlanHooks.LeavingLocal` plus exported `Preflight`/`Postflight`/`AddConfigStep`/`AddEnvNote`; local plans stay in modelsetup and never go through `modelmode.BuildPlan`.
6. **`ApplyStep` owns `ActionTightenToken`** (a chmod) but not `ActionGenerateToken` (needs `RandRead`, an admin Host method).
7. **Observe hook shape**: `ObserveOptions.Local LocalFallback{TokenFile, Listen}`, filled unconditionally by modelsetup; modelmode applies it only in local mode (keeps today's TokenFiles order).
8. **A new `modeview` leaf** for the mode/url/token/server lines both editors show; `editorkit` stays domain-free as designed.
9. **The Setup tab offers local and native only**; remote is set in `wt config`. Avoids a second copy of the remote form; leaving local in `wt-model setup` keeps the bootout/remove prompt.
10. **"Offer to run `wt-model setup`"** is `sessioneditor.Result.RunModelSetup`; cmd/wt-session runs the child after the TUI exits (a TUI cannot host another).
11. **`wt config` reads `config.LoadSession()` with no driver names**: nothing synthesized enters the editor; `writeConfigFile` already drops `AllProviders` (pinned by a new test).
12. **Sync helpers and their printers go into `internal/litellm`** (`syncinputs.go`, `report.go`) in 3a, so both binaries share them during 3b–4c with no copy.
13. **One shared CLI leaf, `internal/cli`**; prompt seams stay per binary and are passed in (`AskOnTTY(open, …)`).
14. **Pointer stubs live in `cmd/wt-session/moved.go`**; only wt-session registers them. Wording: `wt: <old> moved to wt-model — run 'wt-model <new>'; nothing was changed`, with `<old>` without the `wt ` prefix.
15. **Runtime record**: `internal/servestate`, file `<wt-model home>/serve.json`, `Store{Path}` per-call seam, `Remove` only for its own pid; a second concurrent serve overwrites the record (a stop then sees that one; documented).
16. **Session read for host stop uses `GET /control/v1/catalog` `Row.Sessions`** (no API change; no sessions list endpoint exists), via `modelping.GetJSON`.
17. **`wt-model stop <model-id>` is a usage error** naming `wt stop <model>`; wt-model stops providers only.
18. **cmd/wt-model keeps package-var seams for moved commands** (`openTTY`, `probeInventory`, …): converting them is out of scope; new seams (`servestate.Store`, `runtime`) are per-call.
19. **Binary ratchet as a shrinking allow-list** (`sessionBinaryAllow`), introduced in 4d-ii, emptied in 5a — the design's "starts with the cut-loose packages, adds spend and lifecycle" in the allow-list form the ratchet already uses.
20. **`spend.Row`/`Result` become aliases of `modelapi.SpendRow`/`SpendReport`** (identical fields); `WithSessions` goes to `cli`; wt-session drops `WaitPendingRoutes` (it starts no route write once the host commands are gone).
21. **Strings that stay**: `modelclient/drive.go`'s "run 'wt model list --server'" and `job.sample.json` stay correct (`--server` stays in wt-session); `ModeUnsetError`/`NotConfiguredError` texts naming "Model server tab" change to "Mode tab" in 3c (fixtures/tests that pin them move in 3c).
22. **Size**: 1c and 3c exceed ~1,500 changed lines and cannot be cut without a broken intermediate (a half-split Observe; a `wt config` with no local path); they ship as one PR each with reviewable commits. 4a is split into three PRs, 4d into two.

## Deviations from the spine

Changes made after the spine (Appendix A) was frozen, where the real code on `origin/main` f60ad48a disagreed with it. Appendix A has been edited to match; each row says what the code showed.

| Spine § | Spine said | Real code / plan now | Why |
|---|---|---|---|
| §9 | "Each binary keeps `probeInventory` and `syncRoutesAfterWrite` as seams" | `cmd/wt-session` drops each wrapper in the task that removes its last caller: `desiredRouteIDs` (Task 31), `syncRoutesAfterWrite`/`realSyncRoutesAfterWrite`/`seedEnv` (Task 45), `runLitellmSync` and the `probeInventory` declaration (Task 50; Task 37 first moves the declaration from `model_list.go` into wt-session's `litellm.go`, because `model_list.go` is git-mv'd while wt-session still reads it) | kept as written, the wrappers read `probeInventory` (`internal/localmodels`) and import `internal/litellm`, so Task 50's deletion rule never fires and Task 66's empty `sessionBinaryAllow` fails |
| §9 | `noProviderEntryReason` from litellm.go:203; helpers from :200-555 | `const noProviderEntryReason` is at `litellm.go:198`; the cut is :198-555 | line numbers on f60ad48a |
| §10 | `movedError` = `exitcode.New(1, "<text>", false)` | `exitcode.New(1, errors.New("<text>"), false)` | `exitcode.New(code int, err error, quiet bool)` takes an error |
| §3.6 | modelsetup's `BuildPlan`: "then `Postflight`, `restartNote`" on every path | `Postflight` runs once per plan: inside `BuildPlanWith` (native/remote), in modelsetup after `planLocal`; the second `Preflight` inside `BuildPlanWith` is stated as deliberate and idempotent | the spine's wording ran `Postflight` twice on native/remote |
| §13 (4c) | "~1,000 + ~2,000 moved (litellm_test.go splits: not a git mv)" | `litellm_test.go` is git mv'd to `cmd/wt-model/litellm_sync_test.go` and a small wt-session file recreated (Task 50), so the move reads as a rename; a fallback split into 4c-i/4c-ii is named | a cut-and-paste of the 2,299-line file would make 4c about 5,000 changed lines |
| §14 (4a-ii) | the moved `model list`/`model add` rows of `TestHostCommandsStillLoadTheRegistry` "reappear as `cmd/wt-model/host_test.go`" | they are not carried over; `cmd/wt-model/host_test.go` is created in 4a-iii (Task 42) with the served/warm rows, and Tasks 45 and 59 add theirs | no 4a-ii task created the file, while Tasks 45 and 59 called it "4a-ii's" |
| (none; `cmd/wt-model/main.go:30`) | wt-model's root keeps `SilenceUsage: true` | Task 38 drops it and ports wt-session's `silenceUsageOnError` walk | cobra suppresses usage for the whole tree when the root silences it, so the moved `model edit` would lose the usage block it prints today on a usage mistake |

## Revisions after review

- **Remote-to-local rule corrected** (design §modelmode, §sessioneditor, Decisions, Risks; spine §3.6; Task 10 Interfaces and Step 1; Task 30; Review Focus #2 and #4; the guide edit in PR 5; Self-review table): the local pointer plan keeps the stored url and token file only when the stored table's mode is already `local`, and otherwise writes `http://`+`DefaultListen` and `DefaultLocalTokenPath`, so a remote url never becomes local's. New tests `TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal` (Task 10) and `TestModeTabRemoteToLocalNeverKeepsTheRemoteURL` (Task 30); `…RefusesAStoredAtURL` and `…NeedsAConfigHomeForTheDefaultToken` now use a stored `mode = "local"` table.
- **Tasks 66 and 68 greps scoped**: every `hostConfigErr|hostCfgErr` grep is `git grep … -- wt/cmd/wt-session` from the repo root; `cmd/wt-model`'s `host` keeps both names by design.
- **Task 20 keeps `realSyncRoutesAfterWrite`** as a named function in `cmd/wt-session/model.go` (now a one-line call of `litellm.SyncAfterWrite`), so `model_test.go`'s `realRouteSync` still compiles; Task 25's `seams.go` and spine §9 declare the same pair for `cmd/wt-model`, which Task 38's copied `realRouteSync` uses.
- **Task 30 Step 3** counts `modelclient` uses only in the two files it copies (`server_tab.go`, `server_view.go`); the `server_litellm.go` mention is gone.
