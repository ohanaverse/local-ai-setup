# wt: post-exit deferred minors

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the deferred minor findings that survived the `wt-post-exit` code review (plan `docs/superpowers/plans/2026-09-20-wt-post-exit-code-review-fixes.md`) — the unpinned test properties, the dead test data, the one comment whose comparison is inaccurate, the one comment the final review asked to be added, the Makefile's cached test run, and the uniform `nil`→`&config.Config{}` cleanup the original Ruling R8 declined to extend into `internal/tui`.

**Architecture:** Almost all of this is test coverage, not behavior. The picker's `stop.go` and the two remaining source files change only in comments; the runtime code paths are untouched. The test work adds pins for properties that are currently structural-only (a deferred `cancel()`, the absence of a `Stopping …` line for an unattempted model, the drain running *after* a read) plus the first-ever assertion on `survey.ReleaseSession`'s stderr warning branch, which today is guarded only by a byte-identity comparison made by a reviewer.

**Tech Stack:** Go 1.26.7 (module root `wt/`), `go test`, `os.Pipe` for stderr capture, `t.Setenv` for XDG redirection.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-post-exit-design.md` (with the "Revisions" section, which supersedes it where they differ). It is a dated record and is **not** edited by this plan.

**Source of findings:** the ledger for the executed plan above, `.superpowers/sdd/2026-09-20-wt-post-exit-code-review-fixes/progress.md`, which records each minor as it was deferred.

**Out of scope, deliberately:** the four structural findings the review left open — the refcount key being `ModelID` rather than the provider-side name, the untested one-line `stopModel` thunk (finding #4), the ollama/omlx stop-failure asymmetry (finding #5), and the unassigned package-wide `flushTTY` seam (finding #9). Those need design decisions (or a `TestMain`), not a minors pass; they stay open and are reported as such.

**Commit linkage:** this is a new plan with its own items, so its commits carry `- completes plan item #N` for *this* plan's tasks. The executed plan's T1–T7 and its fix round are done and must not be re-referenced.

---

## Global Constraints

- All Go commands run from the `wt/` module root: `cd wt && go build ./... && go vet ./... && go test -count=1 ./...`.
- Every `Test*` keeps a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression) — `wt/CLAUDE.md`, enforced by review, not tooling.
- No test may probe a live provider, stop a real model, or touch the developer's real config. `internal/survey` tests call the unexported `runStopPicker` with a `stopHarness`; the one test that writes to a refcount file must redirect `XDG_CONFIG_HOME` to a `t.TempDir()` first, via `t.Setenv`.
- Test seams stay package-level vars swapped by tests. This plan adds **no** new seam — the `ReleaseSession` test works through `XDG_CONFIG_HOME`, which `config.Dir()` already honors.
- Never weaken an assertion to make a test pass. Every change here either adds a pin or removes data that was never read; none relaxes an existing expectation.
- `gofmt` is a CI gate (`make check` → `go-format-check`). Run `gofmt -l .` from `wt/` before each commit; it must print nothing.
- Dated `docs/superpowers/**` files are historical records and are not edited. Living docs — `wt/docs/**`, `wt/CLAUDE.md` — **are** updated (Task 2).
- No `modelman.toml`/`registry.toml` state changes in this plan, so the `git grep -n "exposed = " docs/guides/` drift check is not triggered.
- **Never** create a PR or push a branch without asking the user first.

---

### Task 1: pin the unpinned properties, and delete test data nothing reads

Five deferred minors, all "the assertion does not pin what its name claims". Each is verified against the current source below — the ledger's line numbers have shifted since it was written.

**Files:**
- Modify: `wt/internal/survey/stop_test.go`
- Modify: `wt/internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: the existing `stopHarness` (`stop_test.go:16-64`), its `cancel`/`cancelOn`/`cancelFails` fields, `runStopPicker`, `flushTTY`, `stopSignalCtx`, and the exported `ReleaseSession`.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Split the drain test and pin that the drain runs *after* a read**

`TestStopPickerFlushesTTYOnEveryReadExit` (`stop_test.go:192-214`) currently loops over `"\n", "q\n", "esc\n", "1\n\n", ""` and asserts one flush for each. Two problems: the `""` input is an *exhausted reader*, not a read exit (the name overclaims), and the counter proves a flush happened but not that it happened after the read — so an implementation draining between `stoppable` and `chooseModels` would pass while eating typed-ahead input on a real TTY.

Add this type above the test (place it directly after `runningEntry`, `stop_test.go:66-68`):

```go
// readTrackingReader counts the reads a caller has issued, so a test can assert
// the drain runs *after* the picker has read — the property that makes it a
// drain of residue rather than a pre-emptive flush that would swallow
// typed-ahead input on a real terminal.
type readTrackingReader struct {
	r     io.Reader
	reads int
}

func (t *readTrackingReader) Read(p []byte) (int, error) {
	t.reads++
	return t.r.Read(p)
}
```

Replace the whole of `TestStopPickerFlushesTTYOnEveryReadExit` with these three tests:

```go
// TestStopPickerDrainsAfterReadingOnEveryExit verifies the paste-residue drain
// runs after the picker has read, on every path that consumed input. A pasted
// block whose first line is "q"/"esc", or whose first line is blank (Enter with
// nothing ticked), leaves every later line in the kernel's TTY input queue,
// where the parent shell runs them as commands once wt exits — the hazard the
// drain exists to prevent. Asserting the ordering (not just the count) is what
// stops a future refactor from draining before the prompt and eating input the
// user typed ahead.
func TestStopPickerDrainsAfterReadingOnEveryExit(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	for _, input := range []string{"\n", "q\n", "esc\n", "1\n\n"} {
		flushes := 0
		src := &readTrackingReader{r: strings.NewReader(input)}
		flushTTY = func() {
			flushes++
			if src.reads == 0 {
				t.Errorf("input %q: drained before the picker read anything", input)
			}
		}
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a")}}}
		runStopPicker(src, &bytes.Buffer{}, &config.Config{}, h.deps())
		if flushes != 1 {
			t.Errorf("input %q: flushes = %d, want 1", input, flushes)
		}
	}
}

// TestStopPickerExhaustedReaderStillDrains verifies the drain is unconditional
// once the picker has offered a choice, even when the reader ends without
// returning a line. On a real terminal the scanner can consume part of the
// input queue and then hit the end, so residue may still be queued; gating the
// drain on "a line was returned" would leave exactly that residue to run as
// shell commands in the parent terminal.
func TestStopPickerExhaustedReaderStillDrains(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	flushes := 0
	flushTTY = func() { flushes++ }
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a")}}}
	runStopPicker(strings.NewReader(""), &bytes.Buffer{}, &config.Config{}, h.deps())
	if flushes != 1 {
		t.Errorf("exhausted reader: flushes = %d, want 1", flushes)
	}
}

// TestStopPickerFlushesNothingWhenNothingOffered verifies a run that offers no
// model stays flush-free: the picker returns before reading a line, so it has
// no residue of its own and must not clear a queue it never consumed.
func TestStopPickerFlushesNothingWhenNothingOffered(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	flushes := 0
	flushTTY = func() { flushes++ }
	h := &stopHarness{snap: localmodels.Snapshot{}}
	runStopPicker(strings.NewReader("\n"), &bytes.Buffer{}, &config.Config{}, h.deps())
	if flushes != 0 {
		t.Errorf("nothing offered: flushes = %d, want 0", flushes)
	}
}
```

- [ ] **Step 2: Pin the deferred `cancel()`, so the signal handler is provably released**

Both cancel tests stub the CancelFunc as `func() {}` (`stop_test.go:226`, `:263`), so deleting `defer cancel()` from `runStopPicker` (`stop.go:144`) keeps the suite green — even though `signal.NotifyContext` holds its handler installed until that CancelFunc runs.

In `TestStopPickerStopIsCancellable`, replace:

```go
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
```

with:

```go
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The picker must call this itself: signal.NotifyContext keeps its handler
	// installed until the returned CancelFunc runs, so dropping the deferred
	// cancel would leave wt's SIGINT handler live past the picker.
	released := false
	stopSignalCtx = func() (context.Context, context.CancelFunc) {
		return ctx, func() { released = true }
	}
```

Then, after the existing `cancelled` assertion at the end of that test (`stop_test.go:245-247`), add:

```go
	if !released {
		t.Error("the picker never called the context's CancelFunc, leaving its signal handler installed")
	}
```

- [ ] **Step 3: Pin that an unattempted model gets no `Stopping …` line**

Still in `TestStopPickerStopIsCancellable`: the loop's top-of-loop guard (`stop.go:147-153`) skips the remaining models, and the test asserts `len(h.stops) != 1` — but nothing checks the *output*, so a guard moved below the `Stopping %s... ` print would still pass while telling the user a stop was attempted when it was not.

Add after the `released` block from Step 2:

```go
	// The guard must skip the print, not just the stop: an unattempted model
	// must not be announced as "Stopping …". Match the print's own prefix —
	// the choice list above legitimately names every offered model.
	if strings.Contains(out.String(), "Stopping ollama/b") {
		t.Errorf("output = %q, want no Stopping line for the unattempted model b", out.String())
	}
```

- [ ] **Step 4: Test `ReleaseSession`'s warning branch for the first time**

`survey.ReleaseSession` (`stop.go:50-54`) is best-effort: on failure it warns on stderr and never fails the exit path. It has **no test** — both call sites go through the `releaseSession` seam, which tests stub, and the executed plan's brief forbade test edits in that area, so a reviewer's byte-identity comparison is currently the only guard on the wording. `config.Dir()` honors `XDG_CONFIG_HOME` and `Release` propagates any non-`IsNotExist` read error, so this is testable with no new seam.

Append to `wt/internal/survey/stop_test.go`, and add `io`, `os`, `path/filepath` to its import block (`stop_test.go:3-12`):

```go
// TestReleaseSessionWarnsWhenStateIsUnwritable verifies a failed refcount
// release warns on stderr and never panics or aborts the exit path. The release
// is best-effort by design — the next launch's Sweep prunes a dead pid's entry —
// but the warning is the user's only signal that the "in use" column may be
// stale, and this branch previously had no coverage at all.
func TestReleaseSessionWarnsWhenStateIsUnwritable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// A *directory* where the state file belongs makes the read fail with
	// EISDIR, which is not os.IsNotExist — the one error class Release
	// propagates rather than swallowing.
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt", "refcount.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = prevStderr })

	ReleaseSession()

	os.Stderr = prevStderr
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !strings.Contains(string(got), "refcount state not released") {
		t.Errorf("stderr = %q, want the refcount release warning", got)
	}
}
```

- [ ] **Step 5: Drop the dead `status` filler from the absent-key row**

`TestProbeTrustedStatuses` (`lifecycle_test.go:453-475`) carries `{"absent key", "", true, true}` — a `status` value that is only read when `absent` is false, so it is dead data a future editor could flip to make the table lie. Move that case out of the table, which also removes the `absent` field and its branch.

Replace the body of `TestProbeTrustedStatuses` (keep its existing doc comment above the function) with:

```go
	// An absent key is trusted, and is the one case with no status to read —
	// asserted separately so the table below holds only rows that set a status.
	if got := ProbeTrusted(localmodels.Snapshot{}, "ollama"); !got {
		t.Error("absent key: ProbeTrusted = false, want true")
	}

	cases := []struct {
		name   string
		status localmodels.Status
		want   bool
	}{
		{"ok", localmodels.StatusOK, true},
		{"partial", localmodels.StatusPartial, false},
		{"unreachable", localmodels.StatusUnreachable, false},
	}
	for _, c := range cases {
		snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": c.status}}
		if got := ProbeTrusted(snap, "ollama"); got != c.want {
			t.Errorf("%s: ProbeTrusted = %v, want %v", c.name, got, c.want)
		}
	}
```

- [ ] **Step 6: Run the affected packages uncached**

Run: `cd wt && go test -count=1 ./internal/survey ./internal/lifecycle -v 2>&1 | tail -40`
Expected: PASS. `stop_test.go` reports 12 tests (was 9: one removed, three added, one new `ReleaseSession` test); `internal/lifecycle` reports the same count as before (the table shrank by one row, which is not a test).

- [ ] **Step 7: Prove the new pins are load-bearing**

Each new assertion must fail against the code it guards, or it is decoration. Verify the two cheapest ones by temporarily reverting the guarded line, running the test, and restoring it — the working tree must be clean (`git diff --exit-code`) before the commit in Step 8:

1. Delete `defer cancel()` from `runStopPicker` (`stop.go:144`) → `TestStopPickerStopIsCancellable` must fail on the `released` assertion. Restore.
2. Move the `fmt.Fprintf(w, "Stopping %s... ", e.ModelID)` line above the `if ctx.Err() != nil` guard (`stop.go:147-155`) → `TestStopPickerStopIsCancellable` must fail on the `Stopping ollama/b` assertion. Restore.

If either still passes, the assertion does not pin what Step 2/Step 3 claims — fix the assertion before committing.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/survey/stop_test.go wt/internal/lifecycle/lifecycle_test.go
git commit -m "test(wt): pin the picker's drain ordering, cancel release, and skip print - completes plan item #1

Four deferred minors were assertions that did not pin what their names
claimed: the drain counter proved a flush happened but not that it
happened after a read, the cancel tests stubbed the CancelFunc so
deleting the deferred cancel kept the suite green, and the skip-path
guard was only checked via the recorded stops, never the output.

Also adds the first test for survey.ReleaseSession's stderr warning
branch, and drops a status filler that an absent-key row never read.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: the comment, doc, Makefile and uniformity minors

The remaining deferred items — none changes runtime behavior. Two are comments on existing code, one is a Makefile flag, one is a uniform nil→value test edit the original Ruling R8 declined, and two are the living docs catching up with the picker's Ctrl+C behavior (Ruling R2's parked item).

**Files:**
- Modify: `wt/internal/survey/stop.go` (comments only)
- Modify: `wt/internal/tui/app_test.go` (three call sites)
- Modify: `wt/internal/tui/postexit_test.go` (two call sites)
- Modify: `Makefile`
- Modify: `wt/docs/wt-agents/README.md`
- Modify: `wt/CLAUDE.md`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: nothing.

- [ ] **Step 1: Correct the seam comment's comparison**

`stopSignalCtx`'s comment (`stop.go:110-113`) claims it "mirrors cmd/wt/start.go's startSignalCtx, in the var/realfunc shape". `start.go:31-34` is an inline closure assigned to the var — not a `var x = realX` pair — and the executed plan's Global Constraints name that exact file as the shape *not* to copy. The code is right; only the comparison is wrong.

Replace:

```go
// stopSignalCtx is a test seam: production uses realStopSignalCtx. It mirrors
// cmd/wt/start.go's startSignalCtx, in the var/realfunc shape wt/CLAUDE.md
// documents for new seams.
var stopSignalCtx = realStopSignalCtx
```

with:

```go
// stopSignalCtx is a test seam: production uses realStopSignalCtx, following the
// var/realfunc shape wt/CLAUDE.md documents for package-level seams. (cmd/wt's
// startSignalCtx covers the same ground for the start path but is an inline
// closure, so what the two share is the intent, not the declaration form.)
var stopSignalCtx = realStopSignalCtx
```

- [ ] **Step 2: Record why Ctrl+C during the menu read is unhandled**

The picker takes its signal context *after* `chooseModels` returns (`stop.go:131` then `:143`), so a Ctrl+C during the menu read gets the default disposition: wt dies and the deferred drain never runs. The final review asked for this to be stated rather than silently left, having advised against installing a handler there — an interrupted tty `read()` returns `EINTR`, which the scanner reads as EOF, silently skipping the prompt the user was answering.

Insert immediately above the `ctx, cancel := stopSignalCtx()` line (`stop.go:141-143`, i.e. between the `if len(selected) == 0 { return }` block and the context line):

```go
	// Ctrl+C during the menu read above is deliberately unhandled: no stop is in
	// flight yet, so there is nothing to cancel, and a handler over that read
	// would turn the interrupted syscall into the scanner's EOF — skipping the
	// prompt mid-answer. The cost is that the default disposition kills wt before
	// the deferred drain runs, leaving any paste residue queued; that is the
	// lesser of the two, and the only path on which it happens.
```

- [ ] **Step 3: Stop the Makefile hiding a cached test run**

`Makefile:35` runs wt's tests without `-count=1`, so `make test-all` reports `(cached)` for packages nothing re-ran — exactly the case where a green run does not mean the tests executed.

Change:

```make
	cd wt && go build ./... && go vet ./... && go test ./...
```

to:

```make
	cd wt && go build ./... && go vet ./... && go test -count=1 ./...
```

Leave the `modelman` line above it unchanged.

- [ ] **Step 4: Make the `nil` cfg uniform in `internal/tui`'s tests**

Ruling R8 declined to cross into this package while the final-review commit was in flight; both packages are now inconsistent, and these five sites read as oversights. The `nil` is latent — `testhelpers_test.go:85` stubs `runStopPhase` package-wide, so `printPendingSummaryAndSurvey` passes `cfg` straight into a stub and never dereferences it — but that is the argument for making them uniform, not for leaving them.

Replace `printPendingSummaryAndSurvey(nil)` with `printPendingSummaryAndSurvey(&config.Config{})` at all five sites:

- `wt/internal/tui/postexit_test.go:50`
- `wt/internal/tui/postexit_test.go:85`
- `wt/internal/tui/app_test.go:1619`
- `wt/internal/tui/app_test.go:1660`
- `wt/internal/tui/app_test.go:1695`

`app_test.go` and `postexit_test.go` both already import `internal/config`, so no import changes are needed. `grep -rn "printPendingSummaryAndSurvey(nil)" wt/internal/tui` must print nothing afterwards.

- [ ] **Step 5: Document the picker's Ctrl+C behavior**

Ruling R2 parked this: the living docs describe the picker's inputs and skip conditions but never its cancellation, which was added by the executed plan's second fix.

In `wt/docs/wt-agents/README.md`, extend the stop-picker bullet (the paragraph ending "would always count as in use.", after line 165) with:

```markdown
  Its stops run on a context of their own, cancelled by Ctrl+C or SIGTERM:
  the stop in flight is abandoned, the remaining models are skipped, and wt
  still prints its summary. Nothing above the picker is watching for those
  signals any more — the agent has already exited — so this is the only
  Ctrl+C handling left on the path.
```

In `wt/CLAUDE.md`, in the post-exit order paragraph, extend the picker sentence that ends "It is silent when nothing qualifies, when stdin is not a TTY, and for command agents." with:

```markdown
Its stops run on a context of their own — Ctrl+C or SIGTERM cancels the stop in flight, skips the remaining models, prints `cancelled`, and still reaches the summary line.
```

Change nothing else in either file. `stopSignalCtx` is already listed in `wt/CLAUDE.md`'s seam list from the executed plan's Task 5, so no seam-list edit is needed.

- [ ] **Step 6: Verify**

Run, from the worktree root:

```bash
cd wt && go build ./... && go vet ./... && gofmt -l . && go test -count=1 ./internal/survey ./internal/tui ./cmd/wt 2>&1 | tail -20
cd .. && grep -rn "printPendingSummaryAndSurvey(nil)" wt/internal/tui
grep -n "stopSignalCtx\|cancelled" wt/CLAUDE.md
```

Expected: build/vet clean; `gofmt -l .` prints nothing; the three packages PASS; the `printPendingSummaryAndSurvey(nil)` grep prints nothing; and the last grep shows the seam-list entry plus the new cancellation sentence.

Then run `make check-links` from the worktree root, since two living docs changed.

- [ ] **Step 7: Commit**

```bash
git add wt/internal/survey/stop.go wt/internal/tui/app_test.go wt/internal/tui/postexit_test.go Makefile wt/docs/wt-agents/README.md wt/CLAUDE.md
git commit -m "docs(wt): correct the seam comment, document the picker's Ctrl+C, force uncached tests - completes plan item #2

The seam comment compared itself to cmd/wt's startSignalCtx, which is an
inline closure rather than the var/realfunc shape the code actually
follows. The picker's cancellation was undocumented (parked as Ruling R2)
and Ctrl+C during the menu read was silently unhandled. make test-all now
passes -count=1 so a green run cannot mean (cached).

Also makes the nil cfg uniform across internal/tui's tests, extending the
fix the final review applied to cmd/wt (Ruling R8).

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: gate the branch

No files change. This is the whole-branch verification the executed plan also ran, repeated because Task 1 touched test files in two packages and Task 2 touched two others.

- [ ] **Step 1: Run the full local gate and capture it to a file**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/wt-post-exit
mkdir -p .superpowers/sdd/2026-09-20-wt-post-exit-deferred-minors
{
  echo "HEAD=$(git rev-parse HEAD)"
  cd wt && go build ./... && go vet ./... && gofmt -l . && go test -count=1 ./...
  echo "STEP1_EXIT=$?"
  cd .. && make test-all
  echo "STEP2_EXIT=$?"
} > .superpowers/sdd/2026-09-20-wt-post-exit-deferred-minors/gate-log.txt 2>&1
tail -20 .superpowers/sdd/2026-09-20-wt-post-exit-deferred-minors/gate-log.txt
```

Expected: both steps exit 0, `gofmt -l .` prints nothing, and the log shows no `FAIL`/`panic`.

- [ ] **Step 2: Report**

Summarize for the user: which minors were closed, which were re-confirmed as no-action (the dated-spec staleness under Ruling R6, the plan-mandated near-duplicate seam comments, the deliberately simplified picker prose, the `VIRTUAL_ENV` uv warning, and the behavioral-not-structural guard), and that the four structural findings remain open. Do **not** push or open a PR — ask first.
