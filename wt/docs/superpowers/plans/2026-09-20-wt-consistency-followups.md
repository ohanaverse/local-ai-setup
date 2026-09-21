# wt consistency and drift follow-ups Implementation Plan (Batch D, issues #126, #121, #120, #122, #129)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the review follow-ups left by PRs #118 and #125: count picker "in use" by provider-side model name (#126), hoist the discovered-under-LiteLLM route refusal into one predicate (#121), confirm #120 is already fixed and pin its remaining gap, trim/pin two TUI/CLI tests (#122), and make the `flushTTY` seam actually package-wide (#129).

**Architecture:** Five independent, small changes in `wt/`. No new packages. #126 adds an exported `lifecycle.SameModel` (rename of the existing unexported `sameModel`) so `internal/survey` matches names the way the engine does. #121 adds one method on `catalog.Row` that the three existing sites call. #129 adds a `TestMain` in `internal/survey`, mirroring `internal/tui` and `cmd/wt`.

**Tech Stack:** Go 1.26.

**Spec:** GitHub issues #126, #121, #120, #122, #129.

## Global Constraints

- All commands run from `wt/`.
- Every test carries a comment saying what it does and why it matters (user rule).
- Do not push or open a PR without asking the user; do not close issues without asking.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`; commits during execution reference the plan item (`- completes plan item #N`).
- New package-level seams follow `var x = realX` + `func realX` (wt/CLAUDE.md).
- **Run after Batch C** (`2026-09-20-wt-lifecycle-stop-and-startable.md`): Task 1 below touches `internal/lifecycle/lifecycle.go` near Batch C's edits.

## File Structure

- Modify: `internal/lifecycle/lifecycle.go` (+ every file using `sameModel`) — export `SameModel`
- Modify: `internal/survey/stop.go` — alias-aware counts
- Modify: `internal/survey/stop_test.go` or `stop_family_test.go` — alias test
- Modify: `internal/catalog/catalog.go`, `internal/tui/modeltable.go`, `cmd/wt/resolve.go`, `internal/smoke/smoke.go` — route refusal predicate
- Modify: `internal/catalog/catalog_test.go` — predicate test
- Modify: `cmd/wt/start_test.go` — generic-failure wording test
- Modify: `internal/tui/modeltable_flow_test.go` (delete a test), `internal/tui/start_flow_test.go`, `cmd/wt/main_test.go` — #122
- Create: `internal/survey/testmain_test.go`; Modify: `internal/survey/prompt.go`, `internal/survey/stop_test.go`, `CLAUDE.md` — #129

---

### Task 1: #126 — count stop-picker usage by provider-side model name

**Files:**
- Modify: `internal/lifecycle/lifecycle.go:97` and all callers of `sameModel` (`gofmt -r` rename)
- Modify: `internal/survey/stop.go` (`stoppable`)
- Test: `internal/survey/stop_family_test.go`

**Interfaces:**
- Produces: `func SameModel(family, a, b string) bool` in `lifecycle` (was unexported `sameModel`; identical behavior).
- Consumes: `stopHarness`, `runningEntry`, `runStopPicker` (test helpers in `stop_test.go`).

- [ ] **Step 1: Write the failing test**

Append to `internal/survey/stop_family_test.go`:

```go
// TestStopPickerAliasRowsShareUsage verifies that two registry rows naming the
// same provider-side model ("qwen3.8" and ollama's implicit "qwen3.8:latest")
// share one usage count: a live session launched from row A keeps row B off
// the list. Counting by registry id alone offered idle alias B, and
// `ollama stop` on it unloaded the single loaded copy under the live session.
func TestStopPickerAliasRowsShareUsage(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/qwen", "qwen3.8"),
			runningEntry("ollama", "ollama/qwen-latest", "qwen3.8:latest"),
			runningEntry("ollama", "ollama/other", "other"),
		}},
		counts: map[string]int{"ollama/qwen": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "ollama|other" {
		t.Fatalf("stops = %v, want only ollama|other (both qwen aliases are in use)", h.stops)
	}
	if strings.Contains(out.String(), "qwen") {
		t.Errorf("output offers an alias of an in-use model: %q", out.String())
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/survey -run TestStopPickerAliasRowsShareUsage -v`
Expected: FAIL — `ollama|qwen-latest` is stopped / offered.

- [ ] **Step 3: Export the matcher**

```bash
gofmt -r 'sameModel -> SameModel' -w internal/lifecycle
```

Then fix its doc comment in `lifecycle.go` to start `// SameModel reports whether two provider-side names denote the same model under the family's matching rule (exported so internal/survey counts usage the way the engine matches occupants).` Run `go build ./... && go test ./internal/lifecycle -count=1`; expected PASS.

- [ ] **Step 4: Make `stoppable` alias-aware**

In `internal/survey/stop.go`, replace the block from `ids := make([]string, len(cands))` through the two `counts[e.ModelID]` uses:

```go
	// Usage is recorded under the registry id a session launched from, but two
	// rows can name one provider-side model (qwen3.8 / qwen3.8:latest). Sum the
	// count over every row that matches this candidate's provider-side name, so
	// a session on either row keeps both off the list.
	aliases := make([][]string, len(cands))
	seen := map[string]bool{}
	var ids []string
	for i, e := range cands {
		aliases[i] = aliasIDs(snap, e)
		for _, id := range aliases[i] {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	counts := d.counts(ids)
	inUse := func(i int) bool {
		for _, id := range aliases[i] {
			if counts[id] > 0 {
				return true
			}
		}
		return false
	}
	// A single-model provider (omlx, mtplx) stops as a whole, taking every
	// running variant with it — so one session using any variant keeps the
	// whole family off the list, not just its own row.
	familyBusy := map[string]bool{}
	for i, e := range cands {
		if inUse(i) && lifecycle.SingleModel(e.ProviderID) {
			familyBusy[localmodels.Family(e.ProviderID)] = true
		}
	}
	var out []localmodels.Entry
	for i, e := range cands {
		if !inUse(i) && !familyBusy[localmodels.Family(e.ProviderID)] {
			out = append(out, e)
		}
	}
	return out
}

// aliasIDs returns the registry ids of every inventory entry in e's provider
// family that names the same provider-side model as e, e's own id included. An
// entry with no model name matches only itself: two empty names must not read
// as "the same model".
func aliasIDs(snap localmodels.Snapshot, e localmodels.Entry) []string {
	fam := localmodels.Family(e.ProviderID)
	ids := []string{e.ModelID}
	if e.ModelName == "" {
		return ids
	}
	for _, o := range snap.Entries {
		if o.ModelID == e.ModelID || o.ModelName == "" || localmodels.Family(o.ProviderID) != fam {
			continue
		}
		if lifecycle.SameModel(fam, o.ModelName, e.ModelName) {
			ids = append(ids, o.ModelID)
		}
	}
	return ids
}
```

(Delete the old `ids`, `counts`, `familyBusy`, and `out` code it replaces; keep the function's doc comment and the earlier candidate loop.)

- [ ] **Step 5: Run**

Run: `go test ./internal/survey ./internal/lifecycle -count=1`
Expected: PASS, including the existing `TestStopPickerStopsOnlySelected` and the single-model family tests.

- [ ] **Step 6: Commit**

```bash
git add -A internal/lifecycle internal/survey
git commit -m "fix(wt): stop picker counts usage across provider-side name aliases - completes plan item #1 (#126)"
```

---

### Task 2: #121 — one predicate for the discovered-under-LiteLLM refusal

**Files:**
- Modify: `internal/catalog/catalog.go` (add method after `BlockReason`)
- Modify: `internal/tui/modeltable.go:125`
- Modify: `cmd/wt/resolve.go:127`
- Modify: `internal/smoke/smoke.go:107-111`
- Test: `internal/catalog/catalog_test.go`

**Interfaces:**
- Produces: `func (r Row) RefusedByRoute(route config.Route, routeErr error) bool` — true iff the row is discovered, its route resolved (`routeErr == nil`), and that route goes through LiteLLM (`route.Litellm || route.Forced`): a model wt found on disk has no entry in the gateway's model list, so it cannot be launched that way. The refusal wording stays at each call site.

- [ ] **Step 1: Write the failing test**

Append to `internal/catalog/catalog_test.go` (add `errors` to imports if absent):

```go
// TestRefusedByRoute verifies the one route-refusal rule: only a discovered
// row whose route resolved AND goes through LiteLLM (routed or forced) is
// refused. Registered rows are never refused, and a route error is not a
// refusal (the picker reports it on Enter instead). The picker, the non-TUI
// -M pin and `wt smoke` all call this, so a change here moves all three.
func TestRefusedByRoute(t *testing.T) {
	direct, viaProxy, forced := config.Route{}, config.Route{Litellm: true}, config.Route{Forced: true}
	cases := []struct {
		name       string
		discovered bool
		route      config.Route
		err        error
		want       bool
	}{
		{"discovered via litellm", true, viaProxy, nil, true},
		{"discovered forced", true, forced, nil, true},
		{"discovered direct", true, direct, nil, false},
		{"discovered with route error", true, viaProxy, errors.New("no route"), false},
		{"registered via litellm", false, viaProxy, nil, false},
	}
	for _, c := range cases {
		if got := (Row{Discovered: c.discovered}).RefusedByRoute(c.route, c.err); got != c.want {
			t.Errorf("%s: RefusedByRoute = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/catalog -run TestRefusedByRoute -v`
Expected: FAIL to compile — `RefusedByRoute undefined`.

- [ ] **Step 3: Implement and delegate**

In `catalog.go`:

```go
// RefusedByRoute reports whether the row must be refused because of how it
// routes: a discovered model — one wt found on disk, absent from the LiteLLM
// gateway's model_list — cannot be launched through the proxy. It is false
// when the route failed to resolve (a launch row with a route error stays
// selectable and reports the error on Enter). The picker table, the non-TUI
// -M pin and `wt smoke` all decide through this one rule; each keeps its own
// wording.
func (r Row) RefusedByRoute(route config.Route, routeErr error) bool {
	return r.Discovered && routeErr == nil && (route.Litellm || route.Forced)
}
```

`internal/tui/modeltable.go`: `case r.Discovered && err == nil && (route.Litellm || route.Forced):` → `case r.RefusedByRoute(route, err):`.

`cmd/wt/resolve.go` (`pickerBlockedReason`): `case row.Discovered && err == nil && (route.Litellm || route.Forced):` → `case row.RefusedByRoute(route, err):`.

`internal/smoke/smoke.go`: replace the inner condition:

```go
			if r.Discovered {
				if route, err := cfg.ResolveRoute(r.Model, agents.ProtocolsFor(a.Name)); r.RefusedByRoute(route, err) {
					continue
				}
			}
```

Update the "mirror" comments at those three sites to say they delegate to `catalog.Row.RefusedByRoute`.

- [ ] **Step 4: Run the regression net**

Run: `go test ./internal/catalog ./internal/tui ./internal/smoke ./cmd/wt -count=1`
Expected: PASS — including `TestResolveModelPinOnDiscoveredUnderLitellmRefuses`, `TestResolveModelPinOnRunningDiscoveredUnderLitellmRefuses`, `TestEligibilityExcludesDiscoveredUnderLitellm` and the modeltable flow tests, which must stay green untouched.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog internal/tui internal/smoke cmd/wt
git commit -m "refactor(wt): single RefusedByRoute predicate for discovered-under-LiteLLM - completes plan item #2 (#121)"
```

---

### Task 3: #120 — verify it is already fixed; pin the remaining generic-failure case

**Finding while planning:** `cmd/wt/start.go` already maps failures through `lifecycle.StartErrorMessage` (`startFailure`, wrapped in `startError` so `errors.Is` still works), and `TestStartForLaunchFailureUsesSharedWording` covers the daemon-down case. It landed in the same commit as #118 (`bf6256e`), so the issue's premise no longer holds. What is *not* pinned is the generic branch (`failed to start <id>: err`).

**Files:**
- Test: `cmd/wt/start_test.go` (append after `TestStartForLaunchFailureUsesSharedWording`)

- [ ] **Step 1: Confirm the claim before acting on it**

Run: `grep -n "startFailure\|StartErrorMessage" cmd/wt/start.go internal/tui/start_flow.go` — expect both paths to call `StartErrorMessage`. Then `go test ./cmd/wt -run 'TestStartForLaunchFailureUsesSharedWording' -v` — expect PASS. If either differs, stop and tell the user; the plan below assumes the finding holds.

- [ ] **Step 2: Add the generic-failure test**

```go
// TestStartForLaunchGenericFailureUsesSharedWording verifies an engine error
// with no dedicated wording is prefixed with the model id exactly as the TUI's
// status line does, while the original error stays on the chain. Only the
// daemon-down mapping was pinned; this closes the other branch of the shared
// StartErrorMessage so the two start paths cannot drift on generic failures.
func TestStartForLaunchGenericFailureUsesSharedWording(t *testing.T) {
	stubSignals(t)
	boom := errors.New("spawn failed")
	stubLifecycleStart(t, []error{boom})

	err := startForLaunch(&config.Config{}, startTestRow(), true)
	want := lifecycle.StartErrorMessage("omlx/qwen3.8", boom)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the engine error still on the chain", err)
	}
}
```

- [ ] **Step 3: Run**

Run: `go test ./cmd/wt -run 'TestStartForLaunch' -count=1 -v`
Expected: PASS (it passes immediately, because behavior already exists; it is a pin, not a fix).

- [ ] **Step 4: Commit**

```bash
git add cmd/wt/start_test.go
git commit -m "test(wt): pin generic start-failure wording on the non-TUI path - completes plan item #3 (#120)"
```

- [ ] **Step 5: Report to the user**

Say #120 was already resolved by #118, and recommend closing it with a comment linking `bf6256e` and the two tests. **Ask before closing.**

---

### Task 4: #122 — drop the redundant test; pin the `--replace` plumbing

**Files:**
- Modify: `internal/tui/modeltable_flow_test.go:201-225` (delete `TestPinnedPathTableReflectsRunningInventory` and its comment block)
- Modify: `internal/tui/start_flow_test.go` (add test after `TestReplaceFlagOnlyCoversThePinnedRow`)
- Modify: `cmd/wt/main_test.go` (add test after `TestWorktreeWithAgentWithoutModelShowsModelPicker`)

- [ ] **Step 1: Confirm the deleted test is a strict subset**

Read `TestPinOnRunningLocalModelLaunches` (`internal/tui/pin_model_test.go:53`). It uses the same fixture and additionally asserts `!it.start` and that the launch branch ran (`launchModel` set). If `TestPinnedPathTableReflectsRunningInventory` asserts anything the other does not (it asserts only `blocked == ""`), fold that line into the survivor instead of deleting outright.

- [ ] **Step 2: Delete the redundant test**

Remove `TestPinnedPathTableReflectsRunningInventory` (the whole function and the comment block above it). Run `go vet ./internal/tui` and remove any imports or helpers that become unused.

- [ ] **Step 3: Write the TUI positive/negative pin**

```go
// TestReplaceFlagGrantsPermissionToPinnedRow verifies the --replace plumbing
// end to end inside the TUI: with allowReplace set, Enter on the -M pinned
// start row reaches the engine with AllowReplace true (no dialog needed), and
// without it the same row starts with AllowReplace false so an occupied
// provider raises the replace dialog. The existing test pins only that a
// non-pinned row does NOT inherit the flag; this pins the positive path, so a
// dropped Run(allowReplace) → model.allowReplace → beginStart wire fails here.
func TestReplaceFlagGrantsPermissionToPinnedRow(t *testing.T) {
	requireBinary(t, "claude")
	for _, allow := range []bool{true, false} {
		m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
		m.allowReplace = allow
		m.pinnedModel = "ollama/gemma4:9b"
		calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })

		got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
		if got.start != nil && got.start.cancel != nil {
			t.Cleanup(got.start.cancel)
		}
		waitStartCalls(t, calls, 1)

		if got := calls.at(0).opts.AllowReplace; got != allow {
			t.Errorf("allowReplace=%v: engine saw AllowReplace=%v, want %v", allow, got, allow)
		}
	}
}
```

- [ ] **Step 4: Write the cmd/wt recorder test**

```go
// TestReplaceFlagReachesTUIRun verifies `--replace` is forwarded from the CLI
// to tui.Run's allowReplace argument, and that omitting it forwards false.
// The parameter is compiler-checked but nothing asserted its value, so a
// dropped or hard-coded flag would silently make --replace a no-op for the
// TUI's -M start path.
func TestReplaceFlagReachesTUIRun(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"with --replace", []string{"-W", "my-feature", "-A", "pi", "--replace"}, true},
		{"without --replace", []string{"-W", "my-feature", "-A", "pi"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := initTestRepo(t)
			oldWd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(oldWd) })
			if err := os.Chdir(dir); err != nil {
				t.Fatalf("chdir: %v", err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("MODELMAN_REGISTRY", "")
			writeEmptyRegistry(t, home)

			// allowReplace is process-wide; restore it so this test cannot leak
			// --replace into another test.
			t.Cleanup(func(prev bool) func() { return func() { allowReplace = prev } }(allowReplace))

			var got, called bool
			oldTuiRun := tuiRun
			tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
				got, called = allowReplace, true
				return nil
			}
			t.Cleanup(func() { tuiRun = oldTuiRun })
			oldStdinTTY := stdinTTY
			stdinTTY = func() bool { return true }
			t.Cleanup(func() { stdinTTY = oldStdinTTY })

			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(c.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Fatal("tuiRun was not called")
			}
			if got != c.want {
				t.Errorf("tuiRun allowReplace = %v, want %v", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 5: Run**

Run: `go test ./internal/tui ./cmd/wt -count=1`
Expected: PASS. Mutation check: temporarily change `tuiRun(yolo(cmd), allowReplace, ...)` at `cmd/wt/main.go:125` to pass `false`; `TestReplaceFlagReachesTUIRun/with_--replace` must FAIL. Restore with `git checkout cmd/wt/main.go`.

- [ ] **Step 6: Commit**

```bash
git add internal/tui cmd/wt/main_test.go
git commit -m "test(wt): pin --replace plumbing to the TUI, drop redundant pinned-path test - completes plan item #4 (#122)"
```

---

### Task 5: #129 — make the `flushTTY` seam genuinely package-wide

**Files:**
- Create: `internal/survey/testmain_test.go`
- Modify: `internal/survey/prompt.go:25`
- Modify: `internal/survey/stop_test.go` (tests that only stub `flushTTY` to silence it)
- Modify: `CLAUDE.md:123-124` (Test seams paragraph)

Decision (issue option 1): add a `TestMain`, and give the seam the documented `var x = realX` shape. The issue's option 2 (fix the docs instead) is rejected: an unassigned seam can flush a developer's real tty under `go test -c` / an IDE, and the two sibling packages already have this guard.

- [ ] **Step 1: Check there is no existing `TestMain`**

Run: `grep -rn "func TestMain" internal/survey/`
Expected: no output. If one exists, add the stub to it instead of creating a file.

- [ ] **Step 2: Reshape the seam**

In `internal/survey/prompt.go`, replace the `flushTTY` declaration and its comment:

```go
// flushTTY discards unread input from the real TTY after the survey finishes
// reading. It targets os.Stdin (not the injected r) because paste residue
// lives in the kernel's input queue for the process's actual fd. A seam
// (var/realfunc, per wt/CLAUDE.md) so tests don't ioctl the test process's
// stdin; this package's TestMain stubs it, so it is a no-op for every test
// that does not assign its own.
var flushTTY = realFlushTTY

func realFlushTTY() { _ = drainTTYInput(int(os.Stdin.Fd())) }
```

- [ ] **Step 3: Add the package `TestMain`**

Create `internal/survey/testmain_test.go`:

```go
package survey

import (
	"os"
	"testing"
)

// TestMain makes the flushTTY seam package-wide: no test in this package may
// drain the developer's real terminal input queue, even when run as a
// hand-built `go test -c` binary or from an IDE that attaches a tty. Tests
// that need to observe a flush still assign flushTTY themselves and restore it
// (the restored value is this no-op). Mirrors internal/tui's and cmd/wt's
// TestMain.
func TestMain(m *testing.M) {
	flushTTY = func() {}
	os.Exit(m.Run())
}
```

- [ ] **Step 4: Remove swap boilerplate that only silences the drain**

In `stop_test.go`, for each test that saves/restores `flushTTY` **only to make it a no-op** (not to count or order calls), delete the `prev := flushTTY` / `t.Cleanup` / assignment lines. Tests that assign a counting closure (`flushes++`, the ordering check around `stop_test.go:211-217`) keep their swap. Inspect each of `stop_test.go` lines ~211, 239, 256, 440 before editing; do not remove a swap that records calls.

- [ ] **Step 5: Update the doc**

In `CLAUDE.md`'s "Test seams" paragraph, keep `flushTTY` in the list, and add after the sentence about the two `TestMain`s: "`internal/survey`'s `TestMain` stubs `flushTTY` the same way, so it is a no-op package-wide." Adjust the "Together the two `TestMain`s" sentence to "the three".

- [ ] **Step 6: Run**

Run: `go test ./internal/survey -count=1 -race && go vet ./internal/survey`
Expected: PASS. Then check the guarantee with a hand-built binary from a real terminal: `go test -c -o /tmp/survey.test ./internal/survey && /tmp/survey.test -test.run TestStopPicker` — type a few characters first, and confirm they are still in the terminal's input queue afterwards (they are not flushed).

- [ ] **Step 7: Commit**

```bash
git add internal/survey CLAUDE.md
git commit -m "test(wt): stub flushTTY package-wide in internal/survey - completes plan item #5 (#129)"
```

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./... -count=1` and `gofmt -l .` (expect no output). Report outputs.
- [ ] `make check-links` from the repo root only if a doc changed (`CLAUDE.md` did, in Task 5).
- [ ] Ask the user before pushing or opening a PR. Suggested PR closes #126, #121, #122, #129; #120 only if the user agrees it is resolved.

## Self-review

- Coverage: each of #126, #121, #120 (verify + pin), #122 (three sub-items), #129 has a task with code.
- Type consistency: `lifecycle.SameModel(family, a, b string) bool`, `aliasIDs(snap, e) []string`, `Row.RefusedByRoute(route config.Route, routeErr error) bool` are named identically wherever used.
- Risks: Task 1 changes which models the picker offers (safer: it can only offer fewer); Task 2 and Task 5 are behavior-preserving; Task 3 adds a pin only. Task 4's `t.Cleanup(func(prev bool) func() {...}(allowReplace))` restores the package-level `allowReplace` global.
