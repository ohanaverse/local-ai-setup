# wt: post-exit flow (#115/#116) — code-review fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve five of the nine findings from the `/code-review` pass over the `wt-post-exit` branch — the ones with a clear, low-risk fix — without changing the post-exit order or the picker's behavior for any input that works today.

**Architecture:** Two of the fixes are in the picker's own control flow (`wt/internal/survey/stop.go`): the paste-residue drain moves to a `defer` so it covers the skip paths as well as the confirm path, and the stop loop gets a signal-aware context instead of `context.Background()` so a wedged provider CLI can be interrupted. The other three remove duplication that this branch introduced: the picker's inlined copy of `lifecycle.probeTrusted` (exported and called instead), the `realReleaseSession` copy-paste shared with `cmd/wt` (one `survey.ReleaseSession`), and two living docs that still describe the pre-change order.

**Tech Stack:** Go 1.26.7 (module root `wt/`), `os/signal` + `syscall` for the picker's context, `go test`.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-post-exit-design.md` (with the "Revisions" section, which supersedes it where they differ) and its executed plan `docs/superpowers/plans/2026-09-20-wt-post-exit.md` (items T1–T7, all complete). Both are dated records and are **not** edited by this plan.

**Source of findings:** the `/code-review` pass over `git diff main...HEAD` on branch `wt-post-exit`.

**Commit linkage:** this is a new plan with its own items, so its commits carry `- completes plan item #N` for *this* plan's tasks (the executed T1–T7 items are done and must not be re-referenced).

---

## Global Constraints

- All Go commands run from the `wt/` module root: `cd wt && go build ./... && go vet ./... && go test -count=1 ./...`.
- Every `Test*` keeps a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression) — `wt/CLAUDE.md`, enforced by review, not tooling.
- No test may probe a live provider or stop a real model. The picker's three dependencies are injected (`stopDeps`); `internal/survey` tests call the unexported `runStopPicker` with a `stopHarness`, never `Picker` with default deps. `TestPickerNoopWhenNotTTY` is the one test that calls the exported `Picker`, and it does so under `withTTY(t, false)` so it returns before building any deps.
- Test seams are package-level vars swapped by tests, and per `wt/CLAUDE.md` a **new** seam takes the documented shape: `var x = realX` plus a `realX` function. (`flushTTY` and `cmd/wt`'s `startSignalCtx` predate that and are anonymous closures — do not copy them; see finding 9 in the notes below.)
- Never weaken an assertion to make a test pass. Migrate, don't delete, in the same commit that breaks them.
- `gofmt` is a CI gate (`make check` → `go-format-check`). Run `gofmt -l .` from `wt/` before each commit; it must print nothing.
- Identifiers that cross task boundaries: `flushTTY` (existing), `stopSignalCtx` / `realStopSignalCtx` (Task 2), `lifecycle.ProbeTrusted` (Task 3), `survey.ReleaseSession` (Task 4). Do not rename one side only.
- Dated `docs/superpowers/**` files are historical records and are not edited. Living docs — `wt/docs/**`, `wt/CLAUDE.md` — **are** updated (Task 5).
- No `modelman.toml`/`registry.toml` state changes in this plan, so the `git grep -n "exposed = " docs/guides/` drift check is not triggered.
- **Never** create a PR or push a branch without asking the user first.

## Findings and dispositions

| # | Finding | Disposition |
|---|---|---|
| 1 | `stop.go:98` — the paste-residue drain is skipped on every early return of `runStopPicker` | Task 1 |
| 8 | `stop.go:116` — the picker's stop runs on `context.Background()`, with no cancellation path | Task 2 |
| 3 | `stop.go:61` — the probe-trust rule is re-implemented inline instead of sharing `lifecycle`'s | Task 3 |
| 7 | `tui/survey.go:49` — `realReleaseSession` is copy-pasted verbatim from `cmd/wt` | Task 4 |
| 6 | `docs/wt-agents/README.md:142`, `docs/wt-stats.md` — living docs describe the pre-change order | Task 5 |
| 2 | `stop.go:85` — refcount is keyed on `ModelID`, so two rows denoting one provider-side model are treated as different models | Not fixed — verified **latent**, see notes |
| 4 | `stopmodel_test.go:58` — asserts only that dispatch reached a fake backend | Not fixed — needs a port-closed fixture; see notes |
| 5 | `ollama.go:31` — trusts the CLI exit code where omlx/mtplx verify the end state | Not fixed — needs a policy decision; see notes |
| 9 | `stop_test.go:86` — the `flushTTY` seam is never assigned, so tests call the real ioctl | Not fixed — verified **latent**; see notes |

## Corrections to the review (each verified against the source before planning)

- **Finding 9 is not the hazard it is described as.** The seam genuinely is unassigned — `grep -rn "flushTTY" --include="*.go" wt` finds only the definition (`prompt.go:25`) and its two call sites, while `wt/CLAUDE.md` lists `flushTTY` among the vars tests swap. But under `go test` the ioctl is a no-op: `os/exec` gives a child with a nil `Stdin` the null device, so `drainTTYInput` fails with `ENOTTY` and the error is discarded. Verified directly — a throwaway module whose test logs `isTTY(os.Stdin.Fd())` prints `false` under `go test`. Impact is limited to running a compiled test binary by hand (`go test -c` then `./survey.test`) or an IDE that attaches a tty. Task 1's new test assigns the seam anyway, because a test that *asserts* a flush happened cannot use the real one.
- **Finding 2 is latent, not live.** The mechanism is exactly as described, but it needs two registry rows denoting the same provider-side model (e.g. `qwen3.8` and `qwen3.8:latest`). `grep -o 'model_name = "[^"]*"' ~/.config/local-ai/registry.toml | sort | uniq -d` returns only `native`, three rows across different providers — none of them a stoppable local. The suggested fix (match on the provider-side name via `localmodels.OllamaNameMatches`/`NameMatches`, as `lifecycle.sameModel` already does) is right and cheap, but it is a behavior change to the refcount rule rather than a defect fix, so it is left for a follow-up with its own test.
- **Finding 5 is real but its fix is a policy choice.** omlx (`omlx.go:46-58`) and mtplx (`mtplx.go:78-85`) both treat "the port is already closed" as success regardless of the CLI's exit code, and ollama (`ollama.go:25-38`) does not. Deciding ollama's success requires choosing between re-probing `/api/ps` (correct, one extra round trip) and treating "model not found" as success (cheap, brittle against ollama's wording) — the user's call, deliberately not made here.

## File Structure

- `wt/internal/survey/stop.go` — `runStopPicker` (Tasks 1, 2), `stoppable` (Task 3), plus the new `ReleaseSession` (Task 4) and the `stopSignalCtx` seam (Task 2).
- `wt/internal/survey/stop_test.go` — the `stopHarness` fixtures and every picker test (Tasks 1, 2).
- `wt/internal/lifecycle/lifecycle.go` — `probeTrusted` → `ProbeTrusted` and its three call sites (Task 3).
- `wt/internal/lifecycle/lifecycle_test.go` — the exported rule's direct test (Task 3).
- `wt/cmd/wt/launch.go`, `wt/internal/tui/survey.go` — the two `realReleaseSession` copies (Task 4).
- `wt/docs/wt-agents/README.md`, `wt/docs/wt-stats.md` — the living docs (Task 5).

---

### Task 1: the picker drains the TTY on every read exit

Finding 1. `flushTTY()` sits at `stop.go:103`, *after* the `len(selected) == 0` return at 99 — so on the paths a pasting user actually hits (`q`/`esc`, or an Enter with nothing ticked), the rest of the pasted block stays in the kernel's input queue for fd 0 and the parent shell executes it as commands once wt exits. That is precisely the hazard the drain's own comment at 101-102 promises to prevent, and it is a regression introduced by this branch: the picker is a second set of reads *after* `PromptRun` already drained its own.

**Files:**
- Modify: `wt/internal/survey/stop.go` (`runStopPicker`, lines 92-123)
- Test: `wt/internal/survey/stop_test.go`

**Interfaces:**
- Consumes: `flushTTY` (`prompt.go:25`, unchanged).
- Produces: nothing other tasks consume. The `defer` stays in place for Task 2's edit of the same function.

- [ ] **Step 1: Write the failing test**

Append to `wt/internal/survey/stop_test.go` (it already imports `bytes`, `strings`, `config`, `localmodels`, and has `stopHarness`, `runningEntry`):

```go
// TestStopPickerFlushesTTYOnEveryReadExit verifies the paste-residue drain runs
// on the picker's skip paths too, not only after a confirmed stop. A pasted
// block whose first line is "q"/"esc", or whose first line is blank (Enter with
// nothing ticked), leaves every later line in the kernel's TTY input queue,
// where the parent shell runs them as commands once wt exits — the hazard the
// drain exists to prevent. A run where nothing is offered must stay flush-free:
// the picker returns before reading a line, so it has no residue of its own.
func TestStopPickerFlushesTTYOnEveryReadExit(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	for _, input := range []string{"\n", "q\n", "esc\n", "1\n\n", ""} {
		flushes := 0
		flushTTY = func() { flushes++ }
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a")}}}
		runStopPicker(strings.NewReader(input), &bytes.Buffer{}, &config.Config{}, h.deps())
		if flushes != 1 {
			t.Errorf("input %q: flushes = %d, want 1", input, flushes)
		}
	}

	flushes := 0
	flushTTY = func() { flushes++ }
	h := &stopHarness{snap: localmodels.Snapshot{}}
	runStopPicker(strings.NewReader("\n"), &bytes.Buffer{}, &config.Config{}, h.deps())
	if flushes != 0 {
		t.Errorf("nothing offered: flushes = %d, want 0", flushes)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/survey -run TestStopPickerFlushesTTYOnEveryReadExit -v`
Expected: FAIL on four inputs — `input "\n": flushes = 0, want 1` (likewise `q\n`, `esc\n`, and ``), while `"1\n\n"` and the nothing-offered case already pass. Those four failures are exactly the finding.

- [ ] **Step 3: Move the drain to a `defer`**

In `wt/internal/survey/stop.go`, replace the selection/early-return block at the top of `runStopPicker` (currently lines 97-103) with:

```go
	selected := chooseModels(bufio.NewScanner(r), w, offered)
	// The picker read lines the same way the survey did; drop any paste
	// residue so it cannot run as shell commands after wt exits. Deferred so it
	// also runs on the skip paths below: "q"/"esc", or an Enter with nothing
	// ticked, leave just as much residue queued as a confirmed stop does, and
	// the early return used to skip the drain entirely.
	defer flushTTY()
	if len(selected) == 0 {
		return
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/survey -v && gofmt -l .`
Expected: PASS, including the pre-existing `TestStopPickerSkipPaths` (it asserts stops, not flushes) and `TestPickerNoopWhenNotTTY`. `gofmt -l .` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/survey/stop.go wt/internal/survey/stop_test.go
git commit -m "fix(survey): drain TTY paste residue on the picker's skip paths too - completes plan item #1

flushTTY sat after the len(selected)==0 early return, so \"q\"/esc and a
blank-line Enter left a pasted block's remaining lines in the kernel input
queue for the parent shell to execute.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: the picker's stop runs on a cancellable context

Finding 8. `d.stop(context.Background(), …)` (`stop.go:116`) has no cancellation path, while `env.run` is `exec.CommandContext` (`wt/internal/lifecycle/env.go:56`). `stopTimeout` bounds the port wait but not the exec'd CLI, so a wedged `ollama stop`/`omlx stop`/`mtplx stop` blocks a wt session that has already finished its agent. Both other lifecycle entry points carry a context (`cmd/wt/start.go:32` builds `signal.NotifyContext`, the TUI's start flow a cancellable one).

**Design.** The context is created *inside* `runStopPicker`, not threaded from the callers, because neither caller has one to give: the TUI's `printPendingSummaryAndSurvey(cfg)` runs after `p.Run()` has returned (and bubbletea's own signal handling is over), and `runAgentCmd` has no context at all. One seam in `internal/survey` therefore serves both paths instead of two callers constructing the same thing — the same reasoning as Task 4. It mirrors `cmd/wt/start.go`'s `startSignalCtx`, in the `var x = realX` form `wt/CLAUDE.md` documents for new seams.

**Behavior change to expect.** Ctrl+C during the picker's stop no longer kills wt outright: it cancels the stop in flight, prints `cancelled`, skips the remaining models and continues to the summary line. That is the point of the finding (a half-finished stop was previously the only outcome available), but it is a visible change and belongs in the commit message.

**Files:**
- Modify: `wt/internal/survey/stop.go` (imports; a new seam above `runStopPicker`; the stop loop in `runStopPicker`)
- Test: `wt/internal/survey/stop_test.go` (`stopHarness`)

**Interfaces:**
- Consumes: the `defer flushTTY()` Task 1 installed in `runStopPicker`.
- Produces: `stopSignalCtx` / `realStopSignalCtx` — the seam Task 2's test swaps. `stopDeps.stop` is unchanged: it already takes a `context.Context`.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/survey/stop_test.go`, extend `stopHarness` — its `stop` closure currently discards the context (`_ context.Context`), which is the whole reason the bug is invisible today. Replace the struct and `deps` method with:

```go
// stopHarness builds picker deps over a fixed snapshot and refcount map and
// records every stop call, so tests assert on exactly what would be stopped.
type stopHarness struct {
	snap   localmodels.Snapshot
	counts map[string]int
	stops  []string // "provider|modelName"
	failOn string   // modelName whose stop fails
	// cancelOn names a modelName whose stop cancels the picker's context, so a
	// test can simulate Ctrl+C arriving while that stop was in flight; cancel is
	// the CancelFunc for the context the test installed via stopSignalCtx.
	cancelOn string
	cancel   context.CancelFunc
	// ctxSeen is the context the picker handed to the first stop, so a test can
	// prove it is cancellable — context.Background() has a nil Done channel.
	ctxSeen context.Context
}

func (h *stopHarness) deps() stopDeps {
	return stopDeps{
		inventory: func(*config.Config) localmodels.Snapshot { return h.snap },
		counts: func(ids []string) map[string]int {
			out := map[string]int{}
			for _, id := range ids {
				out[id] = h.counts[id]
			}
			return out
		},
		stop: func(ctx context.Context, _ *config.Config, provider, name string) error {
			if h.ctxSeen == nil {
				h.ctxSeen = ctx
			}
			h.stops = append(h.stops, provider+"|"+name)
			if h.cancel != nil && name == h.cancelOn {
				h.cancel()
			}
			if name == h.failOn {
				return errors.New("boom")
			}
			return nil
		},
	}
}
```

Then append the test:

```go
// TestStopPickerStopIsCancellable verifies the picker's stops run on a
// cancellable context, and that cancelling it abandons the remaining models
// instead of starting more stops. These stops happen after the agent has exited,
// so this is the only Ctrl+C handling left on the path: on a non-cancellable
// context a wedged provider CLI would block the session with no way out.
func TestStopPickerStopIsCancellable(t *testing.T) {
	prev := stopSignalCtx
	t.Cleanup(func() { stopSignalCtx = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }

	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/b", "b"),
		}},
		cancelOn: "a",
		cancel:   cancel,
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())

	if h.ctxSeen == nil || h.ctxSeen.Done() == nil {
		t.Error("stop ran on a context with no cancellation path (context.Background)")
	}
	if len(h.stops) != 1 || h.stops[0] != "ollama|a" {
		t.Fatalf("stops = %v, want only the first: the second must not start after a cancel", h.stops)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q, want a cancelled line", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/survey -run TestStopPickerStopIsCancellable -v`
Expected: FAIL to build — `undefined: stopSignalCtx`. (With the seam added but `d.stop` still called with `context.Background()`, the `ctxSeen.Done()` assertion is what fails — that is the assertion that pins the finding, so do not drop it.)

- [ ] **Step 3: Add the seam and use it**

In `wt/internal/survey/stop.go`, add `os`, `os/signal`, and `syscall` to the import block (keeping `gofmt`'s grouping), then add directly above `runStopPicker`:

```go
// stopSignalCtx is a test seam: production uses realStopSignalCtx. It mirrors
// cmd/wt/start.go's startSignalCtx, in the var/realfunc shape wt/CLAUDE.md
// documents for new seams.
var stopSignalCtx = realStopSignalCtx

// realStopSignalCtx returns a context cancelled by Ctrl+C or SIGTERM. The
// picker's stops run after the agent has exited, so nothing above it is
// watching for those signals any more: without a context of its own, a wedged
// `ollama stop`/`omlx stop`/`mtplx stop` would block a session that has already
// finished its agent, with no way out but killing the terminal. Cancelling is
// deliberately not fatal — the stop in flight is killed, the rest are skipped,
// and wt still prints its summary.
func realStopSignalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
```

Then, in `runStopPicker`, insert the context after the `len(selected) == 0` return (so a run with nothing selected allocates no signal handler) and replace the stop loop body:

```go
	// These stops run after the agent has exited, so this path owns the only
	// Ctrl+C handling left — see realStopSignalCtx.
	ctx, cancel := stopSignalCtx()
	defer cancel()
	stoppedFamily := map[string]bool{}
	for _, i := range selected {
		if ctx.Err() != nil {
			// Ctrl+C landed while a stop was in flight (or before the first
			// one): complete the current line and stop offering the rest
			// rather than starting stops nobody is waiting for.
			fmt.Fprintln(w, "cancelled")
			break
		}
		e := offered[i]
		fmt.Fprintf(w, "Stopping %s... ", e.ModelID)
		fam := localmodels.Family(e.ProviderID)
		if lifecycle.SingleModel(e.ProviderID) {
			// The first stop already took down the whole provider.
			if stoppedFamily[fam] {
				fmt.Fprintln(w, "done")
				continue
			}
		}
		if err := d.stop(ctx, cfg, e.ProviderID, e.ModelName); err != nil {
			fmt.Fprintf(w, "failed: %v\n", err)
			continue
		}
		stoppedFamily[fam] = true
		fmt.Fprintln(w, "done")
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/survey -v && gofmt -l .`
Expected: PASS. The five pre-existing picker tests must all be green — `TestStopPickerSilentWhenNothingToOffer` (its `"untrusted probe"` case is the one that would catch a cancel check placed before the `ctx.Err()` guard), `TestStopPickerStopsOnlySelected` (its omlx row is what covers the single-model family path), `TestStopPickerSkipPaths`, `TestStopPickerAllAndFailureContinues` (a `failed` line is not a cancel, so both stops must still be attempted), `TestStopPickerInvalidInputReprompts` — plus Task 1's flush test and the new cancellability test. `gofmt -l .` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/survey/stop.go wt/internal/survey/stop_test.go
git commit -m "fix(survey): give the picker's stops a cancellable context - completes plan item #2

The stops run after the agent exits, so context.Background() left a wedged
provider CLI as an unbounded block on the session. Ctrl+C now cancels the
stop in flight and skips the rest instead of killing wt mid-stop.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: one probe-trust rule, exported

Finding 3. `stoppable` re-implements `lifecycle.probeTrusted` inline (`stop.go:61`). The logic is identical today — this is a drift guard, not a live bug: the copy is currently the only option because `probeTrusted` is unexported, which is also why a future tightening of the rule would silently apply to the start/occupant paths and not the stop path. On a single-model provider a stop-side miss is the expensive direction: `StopModel` stops whatever is actually loaded, not the row the user ticked.

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go` (the doc comment and signature at 104-115; call sites at 119, 152, 180)
- Modify: `wt/internal/survey/stop.go` (`stoppable`'s loop, line 61)
- Test: `wt/internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `localmodels.Snapshot`, `localmodels.Status` (both unchanged).
- Produces: `lifecycle.ProbeTrusted(snap localmodels.Snapshot, family string) bool` — called by Task 3's edit of `stoppable` and by anything else that needs the rule.

- [ ] **Step 1: Write the failing test**

Append to `wt/internal/lifecycle/lifecycle_test.go` (it already imports `localmodels` and has the `Snapshot`/`Status` fixtures; there is no direct test of this rule today — it is only exercised through `Start`/`Occupant`):

```go
// TestProbeTrustedStatuses pins the exported probe-trust rule: a family the
// inventory never mentioned counts as trusted (nothing is being started into
// it, and hand-built snapshots carry no status map), StatusOK counts as trusted,
// and every other status does not. The stop picker calls this directly now, so a
// consumer that read it backwards would offer a model whose Running flag no
// probe ever confirmed — and on a single-model provider, stopping that row takes
// down whatever is actually loaded.
func TestProbeTrustedStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status localmodels.Status
		absent bool
		want   bool
	}{
		{"absent key", "", true, true},
		{"ok", localmodels.StatusOK, false, true},
		{"partial", localmodels.StatusPartial, false, false},
		{"unreachable", localmodels.StatusUnreachable, false, false},
	}
	for _, c := range cases {
		snap := localmodels.Snapshot{}
		if !c.absent {
			snap.Providers = map[string]localmodels.Status{"ollama": c.status}
		}
		if got := ProbeTrusted(snap, "ollama"); got != c.want {
			t.Errorf("%s: ProbeTrusted = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestProbeTrustedStatuses -v`
Expected: FAIL to build — `undefined: ProbeTrusted`.

- [ ] **Step 3: Export the rule and call it from the picker**

In `wt/internal/lifecycle/lifecycle.go`, rename the function and extend its doc comment to say who else depends on it, the way `Occupant`'s comment names its caller's interest:

```go
// ProbeTrusted reports whether snap's Running flags for family were produced by
// a probe that could actually determine them. An absent status counts as
// trusted: inventory registers a family's key whenever this config has a local
// provider or model for it, so an absent key means nothing is being started
// into that family — and hand-built snapshots in tests carry no status map.
//
// Exported because the post-exit stop picker (internal/survey) filters its
// candidates by this same rule: it must not offer a model whose running state
// no probe confirmed, or on a single-model provider it would stop whatever is
// actually loaded rather than the row the user ticked. Keeping one
// implementation is the point — a tightened rule here must reach both paths.
func ProbeTrusted(snap localmodels.Snapshot, family string) bool {
```

Update its three call sites in the same file (`isRunning` at 119, `Occupant` at 152, and the one at 180) from `probeTrusted(` to `ProbeTrusted(`. Leave the bodies unchanged.

In `wt/internal/survey/stop.go`, replace the inline rule in `stoppable`:

```go
		// A family whose probe failed reports Running it could not confirm —
		// shared with the start/occupant paths so the rule cannot drift.
		if !lifecycle.ProbeTrusted(snap, localmodels.Family(e.ProviderID)) {
			continue
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle ./internal/survey -v && gofmt -l .`
Expected: PASS — `TestProbeTrustedStatuses` now, plus the pre-existing `TestStopPickerSilentWhenNothingToOffer`'s `"untrusted probe"` case (which covers the picker side of the same rule through `localmodels.StatusPartial`), and the `Occupant`/start tests at `lifecycle_test.go:290-416` that rely on `StatusPartial` making a probe untrusted. `gofmt -l .` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/lifecycle.go wt/internal/lifecycle/lifecycle_test.go wt/internal/survey/stop.go
git commit -m "refactor(lifecycle): export ProbeTrusted and share it with the stop picker - completes plan item #3

The picker held a copy of the rule, so a future tightening would have
applied to the start and occupant paths but not the stop path — where a
miss stops a different model than the one ticked.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: one implementation of the post-exit release

Finding 7. `realReleaseSession` exists twice, byte-identical apart from the seam's name — `wt/internal/tui/survey.go:54-58` and `wt/cmd/wt/launch.go:34-38`, including the `"note: refcount state not released: %v"` stderr string. Changing the release policy (releasing only for non-command agents, adding a timeout, rewording the note) currently has to be done twice or the TUI and non-TUI paths silently diverge.

**Design.** The shared function lives in `internal/survey` next to `Picker`, because `Picker`'s own doc comment already owns the precondition ("The caller must release wt's own refcount entry first, or the model the session just used would always count as in use") and both callers already import the package. Each package keeps its own seam var so the existing tests that swap it (`cmd/wt/testmain_test.go:25`, `postexit_test.go`, `launch_test.go:947`) keep working; only the body moves. `cmd/wt/launch.go` still calls `refcount` directly for its own `Record`, so its imports are unchanged; `internal/tui/survey.go` loses `fmt` and `refcount` entirely.

**Not in scope:** unifying the whole post-exit sequence. The TUI captures its output into `pendingSummary`/`pendingSurveyState` and prints after the alt-screen is torn down while `runAgentCmd` prints directly, so a single shared runner would have to invert that design — a much larger refactor than the duplication it would remove.

**Files:**
- Modify: `wt/internal/survey/stop.go` (new `ReleaseSession`, placed above `Picker`)
- Modify: `wt/cmd/wt/launch.go` (the seam at 28-38)
- Modify: `wt/internal/tui/survey.go` (the seam at 49-58 and the import block)

**Interfaces:**
- Consumes: `refcount.NewStore().Release(pid)` (unchanged).
- Produces: `survey.ReleaseSession()` — the production value of both packages' `releaseSession` seams.

- [ ] **Step 1: Confirm the duplication and its consumers**

Run:
```bash
cd wt && grep -rn "refcount state not released" --include="*.go" .
grep -rn "releaseSession" --include="*_test.go" .
```
Expected: two hits for the message (one per package), and **four** test files swapping `releaseSession` — `cmd/wt/testmain_test.go:25`, `cmd/wt/launch_test.go:947`, `internal/tui/postexit_test.go:19-28`, and `internal/tui/testhelpers_test.go:84`. Every one of them swaps the *seam var*, so all four keep working when only the function body moves; that is why both packages keep their own seam rather than sharing one.

- [ ] **Step 2: Add the shared implementation**

In `wt/internal/survey/stop.go`, immediately above `Picker` (whose doc comment already states the precondition):

```go
// ReleaseSession drops this wt process's own refcount entry, best-effort: a
// failure warns on stderr but never fails the exit path, since the next
// launch's Sweep prunes a dead pid's entry anyway. One implementation for both
// post-exit paths (cmd/wt's runAgentCmd and internal/tui's
// printPendingSummaryAndSurvey), which used to carry identical copies of this
// function and its warning string. Call it BEFORE Picker: the picker offers
// only models no live session uses, so wt's own entry would otherwise always
// count the model this session just used as in use.
func ReleaseSession() {
	if err := refcount.NewStore().Release(os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "note: refcount state not released: %v\n", err)
	}
}
```

`stop.go` already imports `fmt` and `refcount`. Its import block also needs `os` for `os.Getpid()`/`os.Stderr` — Task 2 adds `os` for the signal seam, so it is already present if that task ran first; add it yourself if you are executing this task on its own. (`internal/survey/stop_test.go` needs no import changes for Task 2: it already imports `context` and `errors`.)

- [ ] **Step 3: Point both call sites at it**

In `wt/cmd/wt/launch.go`, replace the seam var and `realReleaseSession` (lines 28-38) with:

```go
// releaseSession is a seam for tests: production releases this wt process's own
// refcount entry (survey.ReleaseSession) so the post-exit stop picker does not
// count the finished session as a user of its model. Best-effort.
var releaseSession = survey.ReleaseSession
```

In `wt/internal/tui/survey.go`, do the same for its copy (lines 49-58):

```go
// releaseSession is a seam for tests: production releases this wt process's own
// refcount entry (survey.ReleaseSession) so the post-exit stop picker does not
// count the finished session as a user of its model. Best-effort.
var releaseSession = survey.ReleaseSession
```

Then remove `"fmt"` and `"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"` from `wt/internal/tui/survey.go`'s import block — `realReleaseSession` was their only user in that file (`os` stays: `realRunSurvey` and `realRunStopPhase` use `os.Stdin`/`os.Stdout`).

- [ ] **Step 4: Verify the copy is gone and the suites still pass**

Run:
```bash
cd wt && go build ./... && go vet ./... && go test -count=1 ./internal/tui ./cmd/wt ./internal/survey -v && gofmt -l .
grep -rn "refcount state not released" --include="*.go" .
```
Expected: build and vet clean; the three suites PASS (including `TestPostExitOrder`, which swaps `releaseSession` and asserts the `release,survey,picker,notice` order — it now swaps a seam whose default is the shared function); `gofmt -l .` prints nothing; the grep prints exactly **one** hit, in `wt/internal/survey/stop.go`.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/survey/stop.go wt/cmd/wt/launch.go wt/internal/tui/survey.go
git commit -m "refactor(survey): one post-exit refcount release for both launch paths - completes plan item #4

realReleaseSession was duplicated verbatim in cmd/wt and internal/tui,
including the stderr string, so release policy had two sources of truth.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: sync the living docs

Finding 6, plus one gap routed here from Task 2's review. `wt/CLAUDE.md` documents the new post-exit *order* correctly (line 66), but three living files are stale on other points:

- `wt/docs/wt-agents/README.md:142` says the survey runs "immediately after the summary line" (it runs before it) and never mentions the picker.
- `wt/docs/wt-stats.md` omits the native-model skip and says the stats block prints "right after answering" (it prints after the stop picker and the summary).
- `wt/CLAUDE.md:120-124` enumerates the package-level test seams by name and lists `flushTTY` but not `stopSignalCtx`, the seam Task 2 added to `internal/survey`.

The original plan's T7 item asked for exactly this class of grep, so the first two are a known gap rather than a new one. The third is a **Ruling** taken on Task 2's review: the reviewer flagged it as outside Task 2's file scope and asked the controller to route it, and a seam-list refresh is living-doc sync — this task's deliverable — so it lands here rather than in a fix round reaching outside its task. Cost if wrong: one doc line in the wrong commit.

**Files:**
- Modify: `wt/docs/wt-agents/README.md` (the paragraph at 142-147)
- Modify: `wt/docs/wt-stats.md` (lines 48-49, 59-62, 64-66)
- Modify: `wt/CLAUDE.md` (the seam list at line 123)

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: nothing.

- [ ] **Step 1: Confirm the drift and its extent**

Run:
```bash
grep -rn "Immediately after the summary line" wt/docs wt/CLAUDE.md
grep -rln "survey" docs/guides/ wt/CLAUDE.md
```
Expected: the first prints `wt/docs/wt-agents/README.md:142` only — `wt/CLAUDE.md` already describes the new order, so it needs no edit. The second prints `wt/CLAUDE.md` but no guide file, which answers the T7 grep: no `docs/guides/**` page documents the post-exit order, so this task is the full extent of the remaining drift.

- [ ] **Step 2: Rewrite the agent reference's post-exit paragraph**

In `wt/docs/wt-agents/README.md`, replace the paragraph at lines 142-147 (beginning "Immediately after the summary line, a post-session survey prompts up to") with:

```markdown
### Post-exit order

The post-run steps run in a fixed order — identical in the TUI and non-TUI
paths, with the user's interactive steps first and the informational output
last so the prompts cannot scroll it away:

> release this session's refcount entry → survey → stop picker → summary
> line → after-survey stats → pricing notice

Both the survey and the stop picker therefore run **before** the summary
line.

- **Survey.** Prompts up to four questions on the parent terminal (did it
  work? speed 1-5? quality 1-5? — and on non-skip answers, what task were
  you doing), each answerable with Enter to skip. It silently does nothing
  when stdin is not a TTY, when the launch had no model (command agents
  like `shell`), or when the model is **native** — a native launch has no
  priced model to survey (issue #116).
- **Stop picker** (`survey.Picker`, issue #115). Offers to stop running
  local models that no live wt session is using (refcount zero, probe
  trusted, stop backend exists). Line-typed: a number toggles, `all`/`none`,
  Enter confirms, `q`/`esc` skips, and nothing starts selected. It is silent
  when nothing qualifies, when stdin is not a TTY, and for command agents.
  wt releases its own refcount entry first, or the model this session just
  used would always count as in use.

See `docs/wt-stats.md` for how the collected survey data is reported.
```

(The inner `>` blockquote is deliberate: it keeps the order readable without a nested code fence.)

- [ ] **Step 3: Fix the stats page**

In `wt/docs/wt-stats.md`, make three edits:

1. Lines 48-49 — name where the prompts sit relative to the deferred output:

```markdown
Every model-driven agent launch (TUI or non-TUI) prompts up to four
questions immediately after the agent exits, before the stop picker and the
summary line:
```

2. Lines 59-62 — add the native skip:

```markdown
The prompt is silent (no output at all) when stdin is not a TTY, when the
launch had no model (command agents like `shell`), or when the model is
native (`config.Model.Native` — a native launch never touches a surveyed,
priced model, issue #116). There is no config toggle to disable it —
every-exit with a one-keypress skip is the intended trade-off.
```

3. Lines 64-66 — the stats block is no longer printed "right after answering":

```markdown
The same accumulated stats `wt stats` reports are printed for the current
model (all agents) and the current agent×model combo, at the 1d/7d/30d
windows — after the stop picker and the summary line, so the interactive
prompts cannot scroll them away.
```

- [ ] **Step 4: Add the new seam to the seam list**

In `wt/CLAUDE.md`, add `stopSignalCtx` to the parenthesized list of package-level var seams at line 123, immediately after `flushTTY` — both are `internal/survey` seams, so keep them adjacent. The sentence ends:

```markdown
`newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`) — production code calls the var, tests swap it.
```

Change nothing else in the paragraph. `stopSignalCtx` is a plain package-level var seam of exactly the shape the paragraph already describes (`var x = realX`, production code calls the var, tests swap it), so one name in the list is the whole change; the prose that follows about `internal/lifecycle`'s `env` struct is a different convention and is untouched.

- [ ] **Step 5: Verify the docs**

Run: `cd .. && make check-links` (from the worktree root — the link checker covers `wt/docs` and the READMEs), then:

```bash
grep -rn "Immediately after the summary line\|Right after answering" wt/docs docs/guides wt/CLAUDE.md
grep -n "stopSignalCtx" wt/CLAUDE.md
```
Expected: `make check-links` passes; the first grep prints nothing; the second prints exactly one hit (the seam list at line 123).

- [ ] **Step 6: Commit**

```bash
git add wt/docs/wt-agents/README.md wt/docs/wt-stats.md wt/CLAUDE.md
git commit -m "docs(wt): document the real post-exit order and the native-model survey skip - completes plan item #5

The agent reference still put the survey after the summary line and never
mentioned the stop picker; the stats page omitted the native skip and
placed the stats block right after answering.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: full local gate

Nothing here changes behavior; it is the verification that the five commits above compose. `internal/survey`, `internal/lifecycle`, `internal/tui`, and `cmd/wt` are the packages touched, and the first two are consumed by the other two.

**Files:** none.

- [ ] **Step 1: Run the whole Go gate from the module root**

Run: `cd wt && go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .`
Expected: build and vet clean; every test passes; `gofmt -l .` prints nothing. Pay attention to `internal/tui` and `cmd/wt`: they consume both refactored packages.

- [ ] **Step 2: Run the repo-wide aggregate**

Run: `cd .. && make test-all`
Expected: PASS — root lint + `check-links`, modelman's `make check`/`make test`, and wt's `go build`/`vet`/`test`. No Python files are touched, so a modelman failure indicates a pre-existing issue; report it rather than fixing it here.

- [ ] **Step 3: Report, and stop short of a PR**

Summarize the five findings fixed and the four left open with the reason (see the notes below), then **ask** whether to push the branch or open a PR. Do not run `gh pr create` or `git push` without explicit approval.

---

## Notes on the findings this plan does not change

Recorded so the review's record is complete and the next reader does not re-open them without new evidence.

- **Finding 2 (`stop.go:85`, refcount keyed on `ModelID`) — real mechanism, latent trigger.** Two registry rows denoting one provider-side model (e.g. `qwen3.8` and `qwen3.8:latest`, which `localmodels.OllamaNameMatches` treats as one artifact) would let an idle alias be offered while a live session uses the other row, and `ollama stop <alias>` unloads the single loaded copy. The current registry has no such duplicate — `grep -o 'model_name = "[^"]*"' ~/.config/local-ai/registry.toml | sort | uniq -d` returns only `native`, three rows across different providers, none stoppable. Worth fixing by matching on the provider-side name (`localmodels.OllamaNameMatches`/`NameMatches`, as `lifecycle.sameModel` does) in a follow-up with a test for the alias pair; it changes the refcount rule, so it does not belong in a review-fix pass.

- **Finding 4 (`stopmodel_test.go:58`) — real, needs a fixture.** `fakeEnv` replaces `e.backends`, so `TestStopModelSingleModelDelegatesToStop` proves only that dispatch reached a backend; a delegation stubbed to `return nil` would keep it green while the picker printed "done" having stopped nothing. Covering it needs `testEnv()` with a stubbed `lookPath`/`run` (as `TestStopModelOllamaSurfacesFailure` does) plus an `httptest` server whose `/v1/models` stops answering, to assert `omlx stop` ran and the port-close wait was performed. That is a test-only change with its own moving parts, so it is left for a follow-up rather than folded into this pass.

- **Finding 5 (`ollama.go:31`) — real asymmetry, needs a policy decision.** omlx and mtplx return `nil` once the port is closed regardless of the CLI's exit code; ollama returns the CLI's message. After a daemon restart or an eviction between probe and stop, `ollama stop X` exits 1 with "model not found" and the picker reports `failed:` for a goal already met. Two fixes are defensible — re-probe the loaded set and treat an absent model as success (correct, one extra round trip) or match ollama's message (cheap, brittle against its wording). Deliberately left to the user.

- **Finding 9 (`stop_test.go:86`, the `flushTTY` seam) — latent, but the documentation is wrong.** The seam is genuinely unassigned, while `wt/CLAUDE.md`'s "Test seams" paragraph lists `flushTTY` among the vars tests swap — so the doc claims a guarantee the package does not provide. Task 1's test assigns the seam for its own run, which fixes it for that test only. Stubbing it package-wide (a `TestMain` in `internal/survey`, mirroring `internal/tui`'s and `cmd/wt`'s) is the small, correct follow-up; it is not a data-loss bug today because `go test` runs the test binary with the null device on stdin, so the ioctl fails and the discarded error makes it a no-op.
