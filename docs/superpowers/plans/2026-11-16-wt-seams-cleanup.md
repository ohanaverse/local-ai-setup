# wt client-session cleanup: collectUsage fold and per-call test seams (#612, #611)

> For agentic workers: use superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** Finish the cleanup bundle from the #607 code review. #608, #609, #610 and #613 were already done on `main` (closed with evidence); this plan covers the two that remain: fold the usage collectors (#612) and migrate the package-level test seams to per-call values (#611).

**No behavior change.** Every task is a refactor: the same user-visible output, exit codes and messages, the same tests passing (tests move from swapping package variables to passing hooks; none is deleted or weakened).

**Spec/authority:** `wt/CLAUDE.md` "Go tests" ("Test seams are per-call values, not package globals ... A package-var seam is the migrating exception, not the rule: convert it to per-call when you touch its package"); issues #611 and #612; the plan sentence in `2026-11-15-wt-session-client.md` Global Constraints that tolerated "still to migrate onto app" seams until this work.

## Global Constraints

- Hooks are **zero-value safe**: a nil/zero hook means the real behavior, so production code and tests that need no stub construct nothing extra. A test that needs a stub sets it on the value it builds (`app`, `model`, options struct), never on a package variable.
- When a task finishes, **no package-level variable is assigned by a test** for the seams it covers: `git grep -nE '^\s*(buildGatewayCmd|buildSlotGatewayCmd|remoteBeatInterval|newModelClient|stopSelectIn|jobSleep|buildGatewayLaunch|buildSlotGatewayLaunch|gatewayRun) *=' -- '*_test.go'` finds nothing for that task's seams, and the `var` declarations are gone (a `func` that returns the real default is fine).
- Parallel safety is the point: tests that used `t.Cleanup` swap-and-restore of a global should become ordinary per-test values; do not add `t.Parallel()` calls beyond what already exists.
- Every `Test*` keeps (or gets) a top-level `//` comment saying what and why; keep each test's assertions equivalent. No fixed ports, no real config home, no real binaries (tests run under the package `TestMain` isolations).
- `cmd/wt-session/boundary_test.go` (session files must not read registry/litellm host state) and `text_lint_test.go` (N7 wording) must keep passing.
- No company-specific details anywhere. Commits end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. PR body uses `Refs #611`/`Refs #612`, never a closing keyword.
- Gates per task: `cd wt && go build ./... && go vet ./... && gofmt -l .` (prints nothing); `go test -count=1` for every touched package; at the end `make lint` and `make -C wt check`. Run the touched packages once with agent binaries off PATH: `env PATH="$(dirname $(which go)):/usr/bin:/bin" go test -count=1 <pkgs>` (CI has none installed; a test that needs a real `claude` fails only there). The full suite (~10 min) runs once at the end, in the background with the exit captured in the log.

---

### Task 1: One usage collector tail (#612)

**Files:** Modify `wt/cmd/wt-session/stats_usage.go`; tests `stats_usage_test.go`, `stats_remote_test.go` (extend, do not weaken).

`collectUsageRemote` and `collectUsageNoServer` repeat the `Narrowed` flag, the `--agent` spend-skipped branch and the `buildUsageRows(usage.NewStore().AllCounts(...), ...)` call. Fold the shared parts into one function that takes the spend outcome as a closure or small value (the builder's call), so a change to how `--agent`/`--model`/`--family` narrow the report is made once. Keep `spendNotConfigured` / `spendUnavailable` / `spendSkipped` semantics and the `spend unavailable: <reason>` text exactly (guide 07 and `wt/docs/wt-stats.md` document them).

- [ ] Step 1: Add a test that pins the shared narrowing (agent set -> spend skipped with `spendSkippedReason` in BOTH the server and no-server paths; model/family set -> `Narrowed`) so the fold cannot drift; run it (it passes on the old code: this is a characterization test).
- [ ] Step 2: Fold; keep both public-in-package entry points' signatures or update their callers (`stats.go`) in the same commit.
- [ ] Step 3: Gate; commit `refactor(wt): one collectUsage tail for the server and no-server reports (#612)`.

### Task 2: tui seams hang off the model (#611, tui part)

**Files:** Modify `wt/internal/tui/remote_start.go` (the `buildGatewayLaunch` and `buildSlotGatewayLaunch` package vars), `wt/internal/tui/app.go` (model fields, `newRunModel`/`Run` if a hook must be passed in), and the tests that swap them (`git grep -nw 'buildGatewayLaunch\|buildSlotGatewayLaunch' -- 'wt/internal/tui/*_test.go'`: about 30 references in 5 files).

**Interfaces:** a small `launchHooks` struct (or two fields) on `model`, zero value = `agents.BuildLaunchCmdGateway` / `agents.BuildSlotLaunchCmdGateway`; accessor methods (`m.buildLaunch()`) return the real function when the field is nil. `tui.Run`'s public signature should not grow for this: hooks are set by tests on the model they construct (the model is already built by `newRunModel` in tests). Update the doc comments that name the old seam, and `wt/docs/internals/tui.md` / `testing.md` if they mention a swapped package var for these.

- [ ] Step 1: Migrate all test call sites to set the hook on the model they build; run the tui tests (RED is not meaningful for a refactor; the evidence is the grep in Global Constraints returning nothing plus green tests).
- [ ] Step 2: Remove the two package vars; gate (touched package `./internal/tui`, also with agents off PATH); commit `refactor(wt): tui launch-build hooks hang off the model (#611)`.

### Task 3: cmd/wt-session and smoke seams hang off app / options (#611, the rest)

**Files:** Modify `wt/cmd/wt-session/{launch_remote.go,slot_launch.go,models.go,model_cmds_remote.go,job.go,client_session.go,smoke.go,app.go}` and `wt/internal/smoke/{smoke_wire.go,smoke.go}`, plus the tests that swap the seams: `buildGatewayCmd` (15 refs/5 files), `buildSlotGatewayCmd` (6/2), `remoteBeatInterval` (3/1), `newModelClient` (30 refs/9 files), `stopSelectIn` (21/2), `jobSleep` (3/1), `gatewayRun` + `SetGatewayRunForTest` (7+4 refs, 3 files).

**Interfaces:** one `hooks` struct on `app` (fields: gateway command builder, slot builder, model-client constructor, heartbeat interval, stop-picker input, job sleep, smoke gateway runner), zero value = the real behavior, with accessor methods so call sites read `a.hooks.client(ep)` etc. Code that has an `*app` uses it directly; code that only has a `*modelSession` (`openClientSession`) gets what it needs from the session (ms already carries the client). For smoke: `RunRowGateway` (fixed signature today) gains a trailing options/run parameter, or the runner is passed through a small struct; `SetGatewayRunForTest` is deleted and its three callers pass the stub per call. The `cmd/wt-session` hooks that wrap the TUI (`tuiRun`) are not in scope unless they are one of the nine named seams.

- [ ] Step 1: Introduce `hooks` and migrate one seam at a time, running `go test -count=1 ./cmd/wt-session -run <affected>` after each (commit-sized checkpoints are fine inside the task; one final commit is also fine).
- [ ] Step 2: Delete the package vars and `SetGatewayRunForTest`; update comments that say "package-variable seam still to migrate" (`launch_remote.go` ~19-23, ~58) and `wt/CLAUDE.md` "Go tests" if the migrating-exception wording should now say these are gone; amend the plan sentence in `docs/superpowers/plans/2026-11-15-wt-session-client.md` only if it still describes these seams as tolerated.
- [ ] Step 3: Gate (touched packages `./cmd/wt-session ./internal/smoke`, also with agents off PATH); commit `refactor(wt): cmd/wt-session and smoke test hooks hang off app and options (#611)`.

### Finish

- [ ] Final whole-branch review; full `go test ./...` in the background (GATE_EXIT in the log), `make lint`, `make -C wt check`; push `chore/wt-seams-cleanup`; open one PR (`Refs #611`, `Refs #612`); do not merge.
