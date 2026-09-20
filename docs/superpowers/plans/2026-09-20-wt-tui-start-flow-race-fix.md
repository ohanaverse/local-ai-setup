# wt: TUI start-flow — fix the `startModel` call-count race

Follow-up to `2026-09-20-wt-tui-start-flow.md` and
`2026-09-20-wt-tui-start-flow-code-review-fixes.md` (both complete). Post-execution
work: commits use a `fix(tui): …` scope tag, not "completes plan item N".

## What CI found

`wt-ci` failed on `ddd73d1` (the code-review-fixes head) in `internal/tui`:

```
--- FAIL: TestReplaceConfirmProceedReissuesStartWithAllowReplace
    start_flow_test.go:269: startModel calls = 1, want 2
--- FAIL: TestKeysDuringStartCancelThenOnlyCtrlCQuits/q
    start_flow_test.go:349: startModel calls = 0, want 1 (cancel does not re-issue)
--- FAIL: TestEndToEndPulledOllamaRowStartsInsteadOfLaunchingCold
    start_flow_test.go:695: startModel calls = 0, want 1
```

All three pass on this machine and fail on CI — a flake, not a regression from the
code-review round. Reproduced deterministically with `GOMAXPROCS=1`, which is the
useful part: this is a **logic race, not a flaky scheduler coincidence**.

## Root cause

`stubStartModel` records the call from inside `startModel`, which `runStart` invokes
on the goroutine it spawns. A test that presses the key that begins a start and then
reads `calls.len()` is asserting about a goroutine it never synchronized with: on a
1–2 core runner the test goroutine's very next instruction usually runs before the
spawned goroutine is scheduled. The mutex on `startCalls` prevents a *data* race (so
`-race` is clean) but does nothing for this **ordering** requirement.

The defect predates the code-review round: `git show dfb7ac9:wt/internal/tui/start_flow_test.go`
carries the same assertions, and `wt-ci` failed on `dfb7ac9` the same way.

Five tests are affected; CI caught three of them:

| Test | Line | Assertion |
|---|---|---|
| `TestEnterOnStartRowBeginsStart` | 160 | `calls == 1` |
| `TestReplaceConfirmProceedReissuesStartWithAllowReplace` | 268 | `calls == 2` |
| `TestKeysDuringStartCancelThenOnlyCtrlCQuits` | 348 | `calls == 1` |
| `TestStaleStartMessagesIgnored` | 582 | `calls == 1` |
| `TestEndToEndPulledOllamaRowStartsInsteadOfLaunchingCold` | 694 | `calls == 1` |

## Design

No production change: the engine *will* be called; the tests just have to wait for it.

`startModel` is recorded before `runStart` sends anything on `ch`, so receiving a
message already implies the call happened — that is why the sites that drain a
message first (e.g. `TestOccupiedOpensReplaceConfirmWithCancelDefault:224`) are
already correct. The sites that fail read the count *before* any drain. Some need
the count without consuming a message (the tests go on to drain `m.start.ch`
themselves), so the fix is a count-based barrier rather than more `recvStart` calls:

```go
// waitStartCalls blocks until startModel has been called n times, failing the
// test if it is not reached in time. The engine runs in the goroutine runStart
// spawns, so a test that asserts on what the engine did must wait for it to have
// done it: reading startCalls straight after the key that began the run asserts
// about a goroutine the test never synchronized with, which passes on a fast
// machine and fails on a 2-core CI runner (GOMAXPROCS=1 reproduces it).
func waitStartCalls(t *testing.T, calls *startCalls, n int)
```

Implementation note: the wait loop must yield (`runtime.Gosched()`), not just sleep —
under `GOMAXPROCS=1` a pure spin can starve the very goroutine being waited on.
Bounded by a deadline (2s, matching `recvStart`) so a genuine regression fails loudly
instead of hanging.

### The two `want 0` sites stay unsynchronized, but get a structural companion

`TestEndToEndAbsentRowIsBlocked:734` and `TestSingleRowShortcutDoesNotFireForStartRow:771`
assert `calls == 0`. A non-event cannot be waited for, so no barrier applies. They are
not flaky — but they are *weak*: they read the count and stop. Both paths would create
`m.start` if they had entered the start flow, so assert `got.start == nil` alongside,
which makes "no start was begun" a property of the state instead of of timing.

## Tasks

- [ ] **Task 1: add `waitStartCalls`** next to `stubStartModel`/`recvStart` in
      `start_flow_test.go`, with the what/why comment above (every `Test*`/helper
      carries one — `wt/CLAUDE.md`).
- [ ] **Task 2: migrate the five assertions** to `waitStartCalls(t, calls, n)` before
      the existing `if calls.len() != n` check, keeping the check (it still catches an
      over-call) and never weakening a message.
- [ ] **Task 3: strengthen the two `want 0` sites** with `got.start == nil`.
- [ ] **Task 4: verify** — `GOMAXPROCS=1 go test ./internal/tui -count=20` (must be
      green; this is the reproduction), then `go build ./... && go vet ./... &&
      go test ./... && go test -race ./internal/tui -count=1 && gofmt -l .` from `wt/`.
- [ ] **Task 5: commit + push** — `fix(tui): wait for the engine goroutine before
      asserting it was called`.

## Why this is worth its own commit

A test that fails on CI but passes locally teaches the team to ignore CI. The
`GOMAXPROCS=1` reproduction makes the fix verifiable rather than hopeful: the fix is
demonstrated by a run that goes from 5 failures to 0, and the same command is the
regression guard for anyone adding another call-count assertion.
