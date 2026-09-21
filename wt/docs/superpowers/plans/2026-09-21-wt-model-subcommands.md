# wt model subcommands (`start`, `stop`, `smoke`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wt start [model]` and `wt stop [model|provider]`, and rework `wt smoke [model]` to start non-running local models and finish with the model-stopping flow.

**Architecture:** Screen 1 (full model picker) is the existing `tui.PickModel` over rows built by `internal/catalog`; screen 2 (stop picker) is the existing line-typed `internal/survey` picker, gaining an in-use-inclusive mode and a reusable stop loop. New commands live in `cmd/wt/model_cmds.go` and reuse the `startModel`/`startForLaunch` driver and the `releaseSession`/`runStopPicker` seams.

**Tech Stack:** Go 1.26 (module root `wt/`), cobra, Bubble Tea. Run every command from `wt/` unless stated.

**Spec:** `wt/docs/superpowers/specs/2026-09-21-wt-model-subcommands-design.md`

## Global Constraints

- Every `Test*` gets a top-level `//` comment stating what it tests and why it matters (wt/CLAUDE.md, user rule).
- Model ids are registry ids `<provider>/<name>` (contain a `/`); provider ids never do. Valid stop providers are those where `lifecycle.CanStop(id)` is true (`ollama`, `omlx`, `omlx-6bit`, `mtplx`).
- Never start/stop real processes or probe live servers in tests: use the seams (`startModel`, `probeInventory`, `pickModelTUI`, `stdinTTY`, `releaseSession`, `runStopPicker`, `smoke.SetSmokeProbeForTest`, survey's `stopDeps`).
- `--replace` is an existing root **persistent** flag; subcommands read it with `cmd.Flags().GetBool("replace")`. Do not redeclare it.
- Exit flows (`wt`, `wt smoke`) keep hiding models in use by a live wt session; only explicit `wt stop` (no arg) lists them.
- Commits during execution end with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>` and reference the plan item (`- completes plan item #N`). Never push or open a PR without asking.
- Mark each task in TaskCreate/TaskUpdate as you go (in_progress → completed).

---

### Task 1: Stop picker — candidates, in-use mode, reusable stop loop (`internal/survey`)

**Files:**
- Modify: `internal/survey/stop.go`
- Test: `internal/survey/stop_test.go`

**Interfaces:**
- Produces:
  - `type Candidate struct { Entry localmodels.Entry; Sessions int }`
  - `func StopCandidates(cfg *config.Config) []Candidate` — every running, trusted, stoppable local model with its live-session count (for a single-model provider the count is the whole family's).
  - `type Options struct { IncludeInUse bool }`
  - `func PickerWith(r io.Reader, w io.Writer, cfg *config.Config, opts Options)` — `Picker(r, w, cfg)` becomes `PickerWith(..., Options{})`.
  - `func StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error` — the picker's stop loop (progress lines, Ctrl+C cancel); returns nil, a `"N of M stops failed"` error, or `errors.New("cancelled")`.
- Unchanged (existing tests call them): `stoppable(cfg, d) []localmodels.Entry`, `runStopPicker(r, w, cfg, d)`, `chooseModels(sc, w, offered)`.

- [ ] **Step 1: Write the failing tests** (append to `internal/survey/stop_test.go`)

```go
// TestStopCandidatesReportsSessionCounts verifies StopCandidates returns every
// running stoppable model INCLUDING ones a live session uses, with the count,
// and that a single-model provider reports its whole family's count. `wt stop`
// needs the in-use models to warn before stopping them; the exit-of-session
// picker must still hide them.
func TestStopCandidatesReportsSessionCounts(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/busy", "busy"),
			runningEntry("omlx", "omlx/x", "x"),
			runningEntry("omlx-6bit", "omlx-6bit/y", "y"),
		}},
		counts: map[string]int{"ollama/busy": 2, "omlx/x": 1},
	}
	got := map[string]int{}
	for _, c := range stopCandidates(&config.Config{}, h.deps()) {
		got[c.Entry.ModelID] = c.Sessions
	}
	want := map[string]int{"ollama/a": 0, "ollama/busy": 2, "omlx/x": 1, "omlx-6bit/y": 1}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("Sessions[%s] = %d, want %d (all: %v)", id, got[id], n, got)
		}
	}
	// The exit-flow view is unchanged: only zero-session models.
	for _, e := range stoppable(&config.Config{}, h.deps()) {
		if e.ModelID != "ollama/a" {
			t.Errorf("stoppable offered %s, want only ollama/a", e.ModelID)
		}
	}
}

// TestStopPickerIncludeInUseMarksSessions verifies that with IncludeInUse the
// picker lists an in-use model with its session count and can stop it. This is
// what lets explicit `wt stop` act on a model while making the risk visible.
func TestStopPickerIncludeInUseMarksSessions(t *testing.T) {
	h := &stopHarness{
		snap:   localmodels.Snapshot{Entries: []localmodels.Entry{runningEntry("ollama", "ollama/busy", "busy")}},
		counts: map[string]int{"ollama/busy": 2},
	}
	var out bytes.Buffer
	runStopPickerWith(strings.NewReader("1\n\n"), &out, &config.Config{}, h.deps(), Options{IncludeInUse: true})
	if !strings.Contains(out.String(), "ollama/busy (2 sessions)") {
		t.Errorf("output = %q, want the session count next to the model", out.String())
	}
	if len(h.stops) != 1 || h.stops[0] != "ollama|busy" {
		t.Errorf("stops = %v, want [ollama|busy]", h.stops)
	}
}

// TestStopEntriesReportsFailures verifies the exported stop loop stops every
// entry, keeps going after a failure, and reports how many failed so a CLI can
// choose a non-zero exit code.
func TestStopEntriesReportsFailures(t *testing.T) {
	h := &stopHarness{failOn: "b"}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}
	err := stopEntries(ctx, &out, &config.Config{}, h.deps(), entries)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 stops failed") {
		t.Fatalf("err = %v, want a 1 of 2 failure", err)
	}
	if len(h.stops) != 2 {
		t.Errorf("stops = %v, want both attempted", h.stops)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/survey -run 'TestStopCandidates|TestStopPickerIncludeInUse|TestStopEntries' -v`
Expected: FAIL — `stopCandidates`, `runStopPickerWith`, `stopEntries` undefined.

- [ ] **Step 3: Implement** in `internal/survey/stop.go`

Replace `stoppable` with a `stopCandidates` core plus a thin `stoppable` wrapper. Keep the existing alias logic verbatim (`cands`, `aliasOf`, `allIDs`, `counts`); only the tail changes:

```go
// Candidate is one running local model wt can stop, with how many live wt
// sessions use it (for a single-model provider: the whole family's count,
// since stopping any variant stops the provider).
type Candidate struct {
	Entry    localmodels.Entry
	Sessions int
}

// StopCandidates is the exported view `wt stop` uses: every running, trusted,
// stoppable model, in-use ones included.
func StopCandidates(cfg *config.Config) []Candidate {
	return stopCandidates(cfg, defaultStopDeps())
}

func stopCandidates(cfg *config.Config, d stopDeps) []Candidate {
	snap := d.inventory(cfg)
	var cands []localmodels.Entry
	for _, e := range snap.Entries { /* unchanged filter: Running, CanStop, ProbeTrusted */ }
	if len(cands) == 0 {
		return nil
	}
	// aliasOf / allIDs / counts: unchanged from the previous stoppable body.
	own := func(e localmodels.Entry) int {
		n := 0
		for _, id := range aliasOf(e) {
			n += counts[id]
		}
		return n
	}
	famSessions := map[string]int{}
	for _, e := range cands {
		if lifecycle.SingleModel(e.ProviderID) {
			famSessions[localmodels.Family(e.ProviderID)] += own(e)
		}
	}
	out := make([]Candidate, 0, len(cands))
	for _, e := range cands {
		n := own(e)
		if lifecycle.SingleModel(e.ProviderID) {
			n = famSessions[localmodels.Family(e.ProviderID)]
		}
		out = append(out, Candidate{Entry: e, Sessions: n})
	}
	return out
}

// stoppable lists the running local models the exit-flow picker may offer:
// stopCandidates with a zero session count.
func stoppable(cfg *config.Config, d stopDeps) []localmodels.Entry {
	var out []localmodels.Entry
	for _, c := range stopCandidates(cfg, d) {
		if c.Sessions == 0 {
			out = append(out, c.Entry)
		}
	}
	return out
}
```

Options and pickers:

```go
// Options tunes the stop picker. IncludeInUse lists models a live wt session
// uses (marked with the count) — for explicit `wt stop`; the exit flows leave
// it false.
type Options struct{ IncludeInUse bool }

func Picker(r io.Reader, w io.Writer, cfg *config.Config) { PickerWith(r, w, cfg, Options{}) }

func PickerWith(r io.Reader, w io.Writer, cfg *config.Config, opts Options) {
	if !stdinTTY() {
		return
	}
	runStopPickerWith(r, w, cfg, defaultStopDeps(), opts)
}

func runStopPicker(r io.Reader, w io.Writer, cfg *config.Config, d stopDeps) {
	runStopPickerWith(r, w, cfg, d, Options{})
}
```

Rewrite `runStopPicker`'s body as `runStopPickerWith(r, w, cfg, d, opts)`:
- Build `cands := stopCandidates(cfg, d)`; `offered` = entries where `opts.IncludeInUse || c.Sessions == 0`; labels = `ModelID`, plus `fmt.Sprintf(" (%d session%s)", n, plural)` when `c.Sessions > 0`. Return silently when `offered` is empty (unchanged).
- Keep the `stopSignalCtx`, `flushTTY` defer, goroutine + `select` on `ctx.Done()` exactly as today, but the goroutine calls `chooseLabeled(bufio.NewScanner(r), w, header, labels)`, where `header` is the current string for the default mode and `"Stop running local models? (sessions = live wt sessions using it)"` when `IncludeInUse`.
- Convert `selected` indices to `entries` and call `stopEntries(ctx, w, cfg, d, entries)`, ignoring the error.

Extract the existing `for _, i := range selected {…}` loop into:

```go
// stopEntries is the picker's stop loop: sequential stops with progress lines,
// one failure never blocks the rest, Ctrl+C (ctx) cancels the stop in flight
// and skips the remainder. Returns nil, a "N of M stops failed" error, or a
// "cancelled" error.
func stopEntries(ctx context.Context, w io.Writer, cfg *config.Config, d stopDeps, entries []localmodels.Entry) error {
	stoppedFamily := map[string]bool{}
	failed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			fmt.Fprintln(w, "cancelled")
			return errors.New("cancelled")
		}
		fmt.Fprintf(w, "Stopping %s... ", e.ModelID)
		fam := localmodels.Family(e.ProviderID)
		if lifecycle.SingleModel(e.ProviderID) && stoppedFamily[fam] {
			fmt.Fprintln(w, "done")
			continue
		}
		if err := d.stop(ctx, cfg, e.ProviderID, e.ModelName); err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(w, "cancelled")
				return errors.New("cancelled")
			}
			fmt.Fprintf(w, "failed: %v\n", err)
			failed++
			continue
		}
		stoppedFamily[fam] = true
		fmt.Fprintln(w, "done")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d stops failed", failed, len(entries))
	}
	return nil
}

// StopEntries runs stopEntries under its own Ctrl+C/SIGTERM context.
func StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error {
	ctx, cancel := stopSignalCtx()
	defer cancel()
	return stopEntries(ctx, w, cfg, defaultStopDeps(), entries)
}
```

Split `chooseModels`/`printChoices` so the existing signatures stay:

```go
const exitFlowHeader = "Stop running local models? (none are in use by another wt session)"

func chooseModels(sc *bufio.Scanner, w io.Writer, offered []localmodels.Entry) []int {
	labels := make([]string, len(offered))
	for i, e := range offered {
		labels[i] = e.ModelID
	}
	return chooseLabeled(sc, w, exitFlowHeader, labels)
}
```

`chooseLabeled(sc, w, header, labels)` is the current `chooseModels` body with `len(offered)` → `len(labels)` and `printChoices(w, header, labels, state)` printing `fmt.Fprintf(w, "  %d [%s] %s\n", i+1, mark, labels[i])`. Add `"errors"` to the imports.

- [ ] **Step 4: Run the whole package**

Run: `go test ./internal/survey -v 2>&1 | tail -30 && go vet ./internal/survey`
Expected: PASS (new and all existing stop tests — the exit-flow wording and behavior are unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/survey/stop.go internal/survey/stop_test.go
git commit -m "feat(wt): stop picker candidates, in-use mode, reusable stop loop - completes plan item #1

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: `wt stop [model|provider]`

**Files:**
- Create: `cmd/wt/model_cmds.go` (stop half; Task 4 adds start)
- Create: `cmd/wt/model_cmds_test.go`
- Modify: `cmd/wt/testmain_test.go` (stub the new seams)
- Modify: `cmd/wt/main.go:~419` (`cmd.AddCommand(...)` — add `stopCmd(a)`)

**Interfaces:**
- Consumes: `survey.StopCandidates`, `survey.StopEntries`, `survey.PickerWith`, `survey.Options`, `survey.Candidate`, `lifecycle.CanStop`, `askYesNo` (cmd/wt/start.go), `stdinTTY`.
- Produces: `func stopCmd(a *app) *cobra.Command`; `func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error`; seams `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`.

- [ ] **Step 1: Stub the seams in TestMain** (`cmd/wt/testmain_test.go`, inside `TestMain` before `os.Exit`)

```go
	// `wt stop` seams: no test may probe live servers or stop a real model.
	stopCandidates = func(*config.Config) []survey.Candidate { return nil }
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		return errors.New("stopEntries not stubbed in this test")
	}
	stopPickerAll = func(*config.Config) {}
	confirmStop = func(string) (bool, error) { return false, nil }
```
Add imports `io` and `github.com/ohanaverse/local-ai-setup/wt/internal/survey`.

- [ ] **Step 2: Write the failing tests** (`cmd/wt/model_cmds_test.go`)

```go
package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// modelCmdConfig is a minimal local-only registry shared by the start/stop
// command tests: two ollama models and one omlx model, no agents needed.
func modelCmdConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "omlx/c", ProviderID: "omlx", ModelName: "c", Location: config.LocationLocal},
		},
	}
	cfg.ExposeAllForTest()
	return cfg
}

func cand(provider, id, name string, sessions int) survey.Candidate {
	return survey.Candidate{
		Entry:    localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true},
		Sessions: sessions,
	}
}

// stubStop swaps the stop seams for one test: candidates is what is "running",
// and the returned slice records every entry passed to the stop loop.
func stubStop(t *testing.T, cands []survey.Candidate) *[]localmodels.Entry {
	t.Helper()
	var stopped []localmodels.Entry
	oc, oe := stopCandidates, stopEntries
	stopCandidates = func(*config.Config) []survey.Candidate { return cands }
	stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	t.Cleanup(func() { stopCandidates, stopEntries = oc, oe })
	return &stopped
}

// TestStopModelIDStopsThatModel verifies `wt stop <provider>/<name>` stops
// exactly that running model and nothing else — the core contract of the command.
func TestStopModelIDStopsThatModel(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
		t.Fatalf("stopped = %v, want only ollama/a:1", *stopped)
	}
}

// TestStopProviderStopsAllItsModels verifies a bare provider id stops every
// running model of that provider and leaves other providers alone; this is the
// "stop everything on ollama" shortcut.
func TestStopProviderStopsAllItsModels(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0), cand("omlx", "omlx/c", "c", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 2 {
		t.Fatalf("stopped = %v, want the two ollama models", *stopped)
	}
}

// TestStopInvalidArgErrors verifies an unknown provider, an unknown model, and
// a registered-but-not-running model each exit with an error and stop nothing.
// A typo must never silently succeed.
func TestStopInvalidArgErrors(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	cases := map[string]string{
		"bogus":          "unknown provider",
		"ollama/nope:9":  "unknown model",
		"ollama/b:1":     "not running",
		"mlx_lm_server":  "unknown provider", // real provider id, but wt has no stop backend
	}
	for arg, want := range cases {
		err := runStop(io.Discard, modelCmdConfig(), arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStop(%q) err = %v, want it to contain %q", arg, err, want)
		}
	}
	if len(*stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", *stopped)
	}
}

// TestStopProviderNothingRunningIsNotAnError verifies `wt stop ollama` with no
// running ollama model prints a note and exits 0, so the command is idempotent.
func TestStopProviderNothingRunningIsNotAnError(t *testing.T) {
	stubStop(t, nil)
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing running") {
		t.Errorf("out = %q, want a nothing-running note", out.String())
	}
}

// TestStopInUseConfirms verifies stopping a model a live wt session uses asks
// first, declining aborts without stopping, accepting (or --yes) stops it.
// This is the safety net for stopping another session's model out from under it.
func TestStopInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)})
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("declined confirm must return an error (non-zero exit)")
	}
	if !strings.Contains(asked, "2") || len(*stopped) != 0 {
		t.Fatalf("asked = %q stopped = %v, want a session-count prompt and no stop", asked, *stopped)
	}

	confirmStop = func(string) (bool, error) { return true, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil || len(*stopped) != 1 {
		t.Fatalf("accepted: err = %v stopped = %v, want one stop", err, *stopped)
	}

	*stopped = nil
	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", true); err != nil || len(*stopped) != 1 {
		t.Fatalf("--yes: err = %v stopped = %v", err, *stopped)
	}
}

// TestStopFailurePropagates verifies a failed stop makes the command fail, so
// scripts can rely on the exit code.
func TestStopFailurePropagates(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("want the stop failure returned")
	}
}

// TestStopNoArgNeedsTTYAndOpensPicker verifies `wt stop` with no argument
// errors without a TTY, says so when nothing is running, and otherwise opens
// the in-use-inclusive picker (screen 2).
func TestStopNoArgNeedsTTYAndOpensPicker(t *testing.T) {
	oldTTY, oldPick := stdinTTY, stopPickerAll
	t.Cleanup(func() { stdinTTY, stopPickerAll = oldTTY, oldPick })
	opened := false
	stopPickerAll = func(*config.Config) { opened = true }

	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	stdinTTY = func() bool { return false }
	if err := runStop(io.Discard, modelCmdConfig(), "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}
	stdinTTY = func() bool { return true }
	if err := runStop(io.Discard, modelCmdConfig(), "", false); err != nil || !opened {
		t.Fatalf("err = %v opened = %v, want the picker opened", err, opened)
	}

	opened = false
	stubStop(t, nil)
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "", false); err != nil || opened || !strings.Contains(out.String(), "no running local models") {
		t.Fatalf("err = %v opened = %v out = %q, want the empty note and no picker", err, opened, out.String())
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./cmd/wt -run 'TestStop' -v`
Expected: FAIL to compile — `runStop`, `stopCandidates`, etc. undefined.

- [ ] **Step 4: Implement** `cmd/wt/model_cmds.go`

```go
// wt start / wt stop — direct control of local models without launching an
// agent. Start reuses the non-TUI start driver (startModel); stop reuses the
// survey package's stop loop and picker.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/spf13/cobra"
)

// Test seams: production talks to the live inventory/providers; cmd/wt's
// TestMain stubs all four.
var (
	stopCandidates = survey.StopCandidates
	stopEntries    = survey.StopEntries
	stopPickerAll  = func(cfg *config.Config) {
		survey.PickerWith(os.Stdin, os.Stdout, cfg, survey.Options{IncludeInUse: true})
	}
	confirmStop = promptStop
)

// promptStop is promptReplace's twin for stopping an in-use model: y/N on the
// controlling terminal, default No, so piped input can never authorise it.
func promptStop(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("%s — rerun with --yes to confirm", question)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

func stopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop [model|provider]",
		Short: "Stop a running local model, or every model of a provider",
		Long: "Stop a running local model (<provider>/<name>) or every running model of a\n" +
			"provider (ollama, omlx, omlx-6bit, mtplx). With no argument, shows the\n" +
			"stop picker (requires a TTY), which also lists models other wt sessions\n" +
			"are using, marked with their session count.\n\n" +
			"Stopping a model a live wt session uses asks for confirmation; --yes skips it.",
		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			yes, _ := cmd.Flags().GetBool("yes")
			return runStop(cmd.OutOrStdout(), a.cfg, arg, yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Skip the confirmation when a target is in use by a live wt session")
	return cmd
}

// runStop implements `wt stop`. Argument errors return before any side effect.
func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error {
	cands := stopCandidates(cfg)
	if arg == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt stop needs a TTY to list models; pass a model or provider (wt stop <provider>/<name> | wt stop <provider>)")
		}
		if len(cands) == 0 {
			fmt.Fprintln(out, "wt: no running local models")
			return nil
		}
		stopPickerAll(cfg)
		return nil
	}

	var targets []survey.Candidate
	if strings.Contains(arg, "/") {
		for _, c := range cands {
			if c.Entry.ModelID == arg {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			if config.IndexModelByID(cfg.Models, arg) >= 0 {
				return fmt.Errorf("model %q is not running", arg)
			}
			return fmt.Errorf("unknown model %q", arg)
		}
	} else {
		if !lifecycle.CanStop(arg) {
			return fmt.Errorf("unknown provider %q (valid: ollama, omlx, omlx-6bit, mtplx)", arg)
		}
		for _, c := range cands {
			if c.Entry.ProviderID == arg {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			fmt.Fprintf(out, "wt: nothing running on %s\n", arg)
			return nil
		}
	}

	inUse := 0
	entries := make([]localmodels.Entry, 0, len(targets))
	for _, c := range targets {
		entries = append(entries, c.Entry)
		if c.Sessions > inUse {
			inUse = c.Sessions
		}
	}
	if inUse > 0 && !yes {
		ok, err := confirmStop(fmt.Sprintf("%s is in use by %d live wt session(s); stop anyway?", arg, inUse))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled — %s is still running", arg)
		}
	}
	return stopEntries(out, cfg, entries)
}
```

In `cmd/wt/main.go` change the last `AddCommand` to `cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a))`.

- [ ] **Step 5: Run and commit**

Run: `go test ./cmd/wt -run 'TestStop' -v && go vet ./cmd/wt && gofmt -l cmd internal`
Expected: PASS, no vet output, gofmt lists nothing.

```bash
git add cmd/wt/model_cmds.go cmd/wt/model_cmds_test.go cmd/wt/testmain_test.go cmd/wt/main.go
git commit -m "feat(wt): wt stop [model|provider] - completes plan item #2

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: Smoke candidates include start rows (`internal/smoke`)

**Files:**
- Modify: `internal/smoke/smoke.go` (`Eligibility`, ~lines 60-125)
- Test: `internal/smoke/smoke_test.go`

**Interfaces:**
- Produces: `type Candidate struct { Row catalog.Row; Agents []string }` (`Row.Model` is the model; `Row.Action()` is `ActionLaunch` or `ActionStart`); `func Candidates(cfg *config.Config) []Candidate` — id-sorted, one per model, agents sorted, includes start rows whose route resolves.
- Unchanged: `Eligibility(cfg)` returns exactly what it does today (launch rows only).

- [ ] **Step 1: Write the failing test** (append to `internal/smoke/smoke_test.go`; reuse whatever config/snapshot fixture helper the file already uses for `Eligibility` tests — read the existing `TestEligibility*` first and copy its setup, adding one registered non-running local model whose snapshot entry is `Registered: true, Artifact: "x", ArtifactKnown: true`, and one whose entry has `Artifact: ""`, `ArtifactKnown: true` (absent))

```go
// TestCandidatesIncludeStartRowsButNotBlocked verifies Candidates lists a
// launchable model, a non-running startable local model (Action start), and
// omits a model missing from disk (blocked). `wt smoke` starts the second kind
// before testing it; Eligibility must keep excluding it so existing callers
// are unchanged.
func TestCandidatesIncludeStartRowsButNotBlocked(t *testing.T) {
	// Arrange with the file's existing fixture: running model R, pulled-but-idle
	// model S, absent model A — all on an ollama provider supported by one agent.
	cfg, restore := candidatesFixture(t)
	defer restore()

	got := map[string]catalog.Action{}
	for _, c := range Candidates(cfg) {
		got[c.Row.Model.ID] = c.Row.Action()
	}
	if got["ollama/running:1"] != catalog.ActionLaunch || got["ollama/idle:1"] != catalog.ActionStart {
		t.Errorf("candidates = %v, want running=launch idle=start", got)
	}
	if _, ok := got["ollama/absent:1"]; ok {
		t.Errorf("blocked (absent) model must not be a candidate: %v", got)
	}
	models, _ := Eligibility(cfg)
	for _, m := range models {
		if m.ID == "ollama/idle:1" {
			t.Error("Eligibility must still exclude start rows")
		}
	}
}
```
Write `candidatesFixture(t)` in the same file: a `config.Config` with an `ollama` local provider (`Auth{Type:"none"}`), three models `running:1`, `idle:1`, `absent:1`, one agent `claude` with `SupportedProviders: []string{"ollama"}`, `cfg.ExposeAllForTest()`, and `SetSmokeProbeForTest` with a snapshot (`Providers: {"ollama": localmodels.StatusOK}`) holding three `Registered` entries: running (`Running: true, ArtifactKnown: true, Artifact: "running:1"`), idle (`Artifact: "idle:1", ArtifactKnown: true`), absent (`Artifact: "", ArtifactKnown: true`). Return `cfg` and the restore func.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/smoke -run TestCandidatesInclude -v`
Expected: FAIL — `Candidates` undefined.

- [ ] **Step 3: Implement.** Extract Eligibility's loop into a shared walker:

```go
// Candidate is one model a smoke run could target: its catalog row (Action is
// launch, or start for a non-running local model wt can start) and the agents
// that could run it once it is up.
type Candidate struct {
	Row    catalog.Row
	Agents []string
}

// Candidates is Eligibility's superset: it also keeps start rows (a non-running
// local model wt can start) whose route resolves, so `wt smoke` can offer them
// and start the pick. Blocked rows stay out. It never starts anything itself.
func Candidates(cfg *config.Config) []Candidate { return walk(cfg, true) }

func walk(cfg *config.Config, includeStart bool) []Candidate {
	snap := smokeProbe(cfg)
	byID := map[string]*Candidate{}
	for _, a := range cfg.Agents {
		if agents.IsCommand(a.Name) {
			continue
		}
		eligible, err := cfg.EligibleModels(a.Name, "", "")
		if err != nil {
			continue
		}
		rows := catalog.Build(catalog.Input{
			Config: cfg, Agent: a.Name, Models: eligible,
			Inventory: &snap, HideDiscovered: false,
		})
		for _, r := range rows {
			act := r.Action()
			if act == catalog.ActionBlock || (act == catalog.ActionStart && !includeStart) {
				continue
			}
			// Keep the exact route rules the old loop applied: a discovered row
			// routed through LiteLLM is refused (smoke must not advertise what a
			// real launch refuses); a start row whose route errors is refused
			// like pickerBlockedReason does. A launch row with a route error
			// stays eligible (the picker reports it on Enter).
			if r.Discovered || act == catalog.ActionStart {
				route, rerr := cfg.ResolveRoute(r.Model, agents.ProtocolsFor(a.Name))
				if r.Discovered && r.RefusedByRoute(route, rerr) {
					continue
				}
				if act == catalog.ActionStart && rerr != nil {
					continue
				}
			}
			c := byID[r.Model.ID]
			if c == nil {
				c = &Candidate{Row: r}
				byID[r.Model.ID] = c
			}
			c.Agents = append(c.Agents, a.Name)
		}
	}
	out := make([]Candidate, 0, len(byID))
	for _, c := range byID {
		sort.Strings(c.Agents)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Row.Model.ID < out[j].Row.Model.ID })
	return out
}

func Eligibility(cfg *config.Config) (models []config.Model, agentsForModel map[string][]string) {
	agentsForModel = map[string][]string{}
	for _, c := range walk(cfg, false) {
		models = append(models, c.Row.Model)
		agentsForModel[c.Row.Model.ID] = c.Agents
	}
	return models, agentsForModel
}
```
Keep `Eligibility`'s existing doc comment, but drop its "never starts anything — starting … is the launch path's job" sentence and say instead: "It never starts or stops anything itself; `Candidates` plus the caller's start step is how `wt smoke` handles idle local models." Update the `Eligibility` callers' expectations: none change.

- [ ] **Step 4: Run all smoke tests**

Run: `go test ./internal/smoke -v 2>&1 | tail -20 && go vet ./internal/smoke`
Expected: PASS (existing Eligibility tests unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/smoke/smoke.go internal/smoke/smoke_test.go
git commit -m "feat(wt): smoke.Candidates includes startable local models - completes plan item #3

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: Screen 1 blocked rows + `wt start [model]`

**Files:**
- Modify: `internal/tui/pick_model.go` (`pickModel` struct, `Update`, `View`)
- Test: `internal/tui/pick_model_test.go`
- Modify: `cmd/wt/model_cmds.go` (add start half)
- Modify: `cmd/wt/model_cmds_test.go`
- Modify: `cmd/wt/main.go` (`AddCommand(... startCmd(a))`)

**Interfaces:**
- Consumes: `catalog.Build/Find/Row.Action/BlockReason`, `probeInventory`, `startModel` (`func(*config.Config, catalog.Row, bool) error`), `pickModelTUI` (`func(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error)`), `stdinTTY`.
- Produces: `func startCmd(a *app) *cobra.Command`; `func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error`; `func localRows(cfg *config.Config) []catalog.Row`.

- [ ] **Step 1: Failing tui test** (`internal/tui/pick_model_test.go`)

```go
// TestPickModelEnterOnBlockedRowDoesNotSelect verifies pressing Enter on a
// blocked row (e.g. a model missing from disk) keeps the picker open and shows
// the reason, while Enter on a normal row selects it. `wt start` lists blocked
// rows for visibility; letting Enter pick one would start a doomed model.
func TestPickModelEnterOnBlockedRowDoesNotSelect(t *testing.T) {
	items := []list.Item{
		&modelItem{model: config.Model{ID: "ollama/gone"}, blocked: "ollama/gone is not on disk — pull or download it first"},
		&modelItem{model: config.Model{ID: "ollama/ok"}},
	}
	m := pickModel{list: list.New(items, list.NewDefaultDelegate(), 80, 24)}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm := next.(pickModel)
	if cmd != nil || pm.selected.ID != "" || !strings.Contains(pm.notice, "not on disk") {
		t.Fatalf("blocked Enter: selected=%q notice=%q cmd=%v, want no selection and a notice", pm.selected.ID, pm.notice, cmd)
	}
	pm.list.CursorDown()
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := next.(pickModel).selected.ID; got != "ollama/ok" {
		t.Fatalf("selected = %q, want ollama/ok", got)
	}
}
```
Add imports as needed (`strings`, `bubbles/list`, `tea`, `config`).

- [ ] **Step 2: Run** `go test ./internal/tui -run TestPickModelEnterOnBlocked -v` — Expected: FAIL (`notice` undefined).

- [ ] **Step 3: Implement.** In `pick_model.go`: add `notice string` to `pickModel`; in `Update`'s `"enter"` branch, after the `SelectedItem` assertion:

```go
if it.blocked != "" {
	m.notice = it.blocked
	return m, nil
}
```
and clear `m.notice = ""` at the top of the `tea.KeyMsg` case. In `View`: 

```go
v := m.list.View()
if m.notice != "" {
	v += "\n" + m.notice
}
return v
```
Update `PickModel`'s and `newPickModel`'s comments: the caller's `models` may now include start and blocked rows; blocked rows are shown but not selectable.

- [ ] **Step 4: Failing cmd tests** (append to `cmd/wt/model_cmds_test.go`)

```go
// startFixture stubs the inventory: ollama/a:1 running, ollama/b:1 pulled but
// idle (start row), omlx/c missing from disk (blocked). Returns the start recorder.
func startFixture(t *testing.T) (*config.Config, *startRequest) {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "a:1", ModelID: "ollama/a:1", ModelName: "a:1", Registered: true, Running: true, ArtifactKnown: true},
			{ProviderID: "ollama", Artifact: "b:1", ModelID: "ollama/b:1", ModelName: "b:1", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", Artifact: "", ModelID: "omlx/c", ModelName: "c", Registered: true, ArtifactKnown: true},
		},
	})
	return modelCmdConfig(), stubStartDriver(t, nil)
}

// TestStartRunningModelIsNoOp verifies `wt start` on an already-running model
// says so and never calls the start driver — starting twice must be harmless.
func TestStartRunningModelIsNoOp(t *testing.T) {
	cfg, req := startFixture(t)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called || !strings.Contains(out.String(), "already running") {
		t.Fatalf("called = %v out = %q, want a no-op note", req.called, out.String())
	}
}

// TestStartIdleModelStartsIt verifies an idle local model goes through the
// start driver with the caller's --replace permission.
func TestStartIdleModelStartsIt(t *testing.T) {
	cfg, req := startFixture(t)
	if err := runStart(io.Discard, cfg, themes.Theme{}, "ollama/b:1", true); err != nil {
		t.Fatal(err)
	}
	if !req.called || req.row.Model.ID != "ollama/b:1" || !req.replace {
		t.Fatalf("req = %+v, want ollama/b:1 started with replace", req)
	}
}

// TestStartInvalidArgErrors verifies unknown ids, cloud ids, and blocked models
// exit with an error naming the problem and never start anything.
func TestStartInvalidArgErrors(t *testing.T) {
	cfg, req := startFixture(t)
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
	cases := map[string]string{"ollama/nope:9": "unknown model", "openrouter/x": "not a local model", "omlx/c": "not on disk"}
	for arg, want := range cases {
		err := runStart(io.Discard, cfg, themes.Theme{}, arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStart(%q) err = %v, want %q", arg, err, want)
		}
	}
	if req.called {
		t.Error("nothing may be started for an invalid argument")
	}
}

// TestStartNoArgUsesPickerAndRunningPickIsNoOp verifies the no-arg flow needs a
// TTY, lists every local model via the shared picker, starts an idle pick, and
// treats a running pick as a no-op (spec: selecting a running model does nothing).
func TestStartNoArgUsesPickerAndRunningPickIsNoOp(t *testing.T) {
	cfg, req := startFixture(t)
	oldTTY, oldPick := stdinTTY, pickModelTUI
	t.Cleanup(func() { stdinTTY, pickModelTUI = oldTTY, oldPick })

	stdinTTY = func() bool { return false }
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}

	stdinTTY = func() bool { return true }
	var offered []string
	pick := "ollama/b:1"
	pickModelTUI = func(_ *config.Config, models []config.Model, _ themes.Theme) (config.Model, bool, error) {
		for _, m := range models {
			offered = append(offered, m.ID)
		}
		return config.Model{ID: pick}, true, nil
	}
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || !req.called {
		t.Fatalf("idle pick: err = %v called = %v, want a start", err, req.called)
	}
	if len(offered) != 3 {
		t.Errorf("picker offered %v, want all three local models (running, idle, blocked)", offered)
	}

	*req = startRequest{}
	pick = "ollama/a:1"
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || req.called {
		t.Fatalf("running pick: err = %v called = %v, want a no-op", err, req.called)
	}
}
```
Add imports `themes`. Also `TestStartCancelledPicker`: `pickModelTUI` returning `ok=false` → error containing `canceled`; include it in the same test file (3 lines).

- [ ] **Step 5: Run** `go test ./cmd/wt -run 'TestStart(Running|Idle|Invalid|NoArg)' -v` — Expected: FAIL (`runStart` undefined).

- [ ] **Step 6: Implement** (append to `model_cmds.go`; add imports `catalog`, `themes`)

```go
func startCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every configured and detected local model,\n" +
			"running ones included (requires a TTY); picking a running model does nothing.\n\n" +
			"If the provider's single slot is occupied, asks before replacing the running\n" +
			"model; --replace skips the question.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			return runStart(cmd.OutOrStdout(), a.cfg, a.theme, id, replace)
		},
	}
	return cmd
}

// localRows builds catalog rows for every configured and detected local model
// from one live inventory snapshot — screen 1's row set.
func localRows(cfg *config.Config) []catalog.Row {
	snap := probeInventory(cfg)
	var models []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			models = append(models, m)
		}
	}
	return catalog.Build(catalog.Input{Config: cfg, Models: models, Inventory: &snap})
}

// runStart implements `wt start`. Argument errors return before any side effect.
func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error {
	rows := localRows(cfg)
	if id == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt start needs a TTY to list models; pass a model id directly (wt start <provider>/<name>)")
		}
		if len(rows) == 0 {
			return fmt.Errorf("no local models are configured or detected")
		}
		models := make([]config.Model, len(rows))
		for i, r := range rows {
			models[i] = r.Model
		}
		m, ok, err := pickModelTUI(cfg, models, theme)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("model selection canceled")
		}
		id = m.ID
	}
	row, ok := catalog.Find(rows, id)
	if !ok {
		if config.IndexModelByID(cfg.Models, id) >= 0 {
			return fmt.Errorf("%q is not a local model — wt start only starts local models", id)
		}
		return fmt.Errorf("unknown model %q", id)
	}
	switch row.Action() {
	case catalog.ActionStart:
		if err := startModel(cfg, row, replace); err != nil {
			return err
		}
		fmt.Fprintf(out, "wt: %s is running\n", id)
		return nil
	case catalog.ActionLaunch:
		fmt.Fprintf(out, "wt: %s is already running\n", id)
		return nil
	}
	return fmt.Errorf("%s", row.BlockReason())
}
```
Register `startCmd(a)` in `main.go`'s `AddCommand`.

- [ ] **Step 7: Run and commit**

Run: `go test ./internal/tui ./cmd/wt 2>&1 | tail -20 && go vet ./... && gofmt -l cmd internal`
Expected: PASS, no vet/gofmt output.

```bash
git add internal/tui/pick_model.go internal/tui/pick_model_test.go cmd/wt/model_cmds.go cmd/wt/model_cmds_test.go cmd/wt/main.go
git commit -m "feat(wt): wt start [model] with blocked-row-aware picker - completes plan item #4

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 5: Rework `wt smoke` (start-then-smoke, exit stop flow)

**Files:**
- Modify: `cmd/wt/smoke.go` (`RunE`, `resolveSmokeModel`)
- Modify: `cmd/wt/smoke_test.go`

**Interfaces:**
- Consumes: `smoke.Candidates`, `catalog.ActionStart`, `startModel`, `releaseSession`, `runStopPicker`, `pickModelTUI`, `stdinTTY`.
- Produces: `type smokeTarget struct { Row catalog.Row; Agents []string }` (method `Start() bool` → `Row.Action() == catalog.ActionStart`); `resolveSmokeModel(cfg *config.Config, theme themes.Theme, modelID string) (smokeTarget, error)`; `func smokeStopFlow(cfg *config.Config, jsonOut bool)`; `func runSmoke(cmd *cobra.Command, a *app, args []string) (anyFail bool, err error)`.

- [ ] **Step 1: Update existing tests + add new ones** in `cmd/wt/smoke_test.go`

Existing `TestResolveSmokeModel*` tests destructure `(m, agents, err)`; change them to read `t.Row.Model` and `t.Agents`. `TestResolveSmokeModelPinnedNotEligible` used the idle registered local model `not-eligible:x`, which is now a **start** target, so repurpose it: add to `smokeFixtureConfig` a third model whose snapshot entry is absent (`Registered: true, Artifact: "", ArtifactKnown: true`) named `ollama/gone:x`, and assert pinning it errors mentioning "cannot be smoke-tested". Add:

```go
// TestResolveSmokeModelIdleLocalIsStartTarget verifies a registered local model
// that is pulled but not running now resolves (with Start() true) instead of
// erroring, since `wt smoke` starts it before testing.
func TestResolveSmokeModelIdleLocalIsStartTarget(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	got, err := resolveSmokeModel(cfg, themes.Theme{}, "ollama/not-eligible:x")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start() || len(got.Agents) == 0 {
		t.Fatalf("target = %+v, want a start target with agents", got)
	}
}

// TestSmokeStopFlow verifies the exit flow releases the refcount, then runs the
// stop picker, and is skipped for --json or without a TTY (JSON consumers and
// pipes must not get an interactive prompt).
func TestSmokeStopFlow(t *testing.T) {
	oldRel, oldPick, oldTTY := releaseSession, runStopPicker, stdinTTY
	t.Cleanup(func() { releaseSession, runStopPicker, stdinTTY = oldRel, oldPick, oldTTY })
	var calls []string
	releaseSession = func() { calls = append(calls, "release") }
	runStopPicker = func(*config.Config) { calls = append(calls, "picker") }

	stdinTTY = func() bool { return true }
	smokeStopFlow(nil, false)
	if strings.Join(calls, ",") != "release,picker" {
		t.Fatalf("calls = %v, want release then picker", calls)
	}
	calls = nil
	smokeStopFlow(nil, true)
	stdinTTY = func() bool { return false }
	smokeStopFlow(nil, false)
	if len(calls) != 0 {
		t.Fatalf("calls = %v, want none for --json / no TTY", calls)
	}
}
```
And an end-to-end command test using the cobra command with `smokeExit` and `buildAndRun` stubbed — mirror `TestSmokeCmdFlags`'s construction; check how `internal/smoke` exposes `buildAndRun` for tests (`SetBuildAndRunForTest` may not exist; if absent, add a small exported test hook next to `SetSmokeProbeForTest` in `internal/smoke/smoke.go` following the same pattern, with a doc comment "Tests only"). The test asserts, for an idle pin: `startModel` recorder called once **before** the row runs; the stop flow ran once afterwards; and with the stubbed row returning FAIL, `smokeExit` recorded `1` **after** the stop flow ran.

- [ ] **Step 2: Run** `go test ./cmd/wt -run 'Smoke' -v` — Expected: FAIL/compile errors.

- [ ] **Step 3: Implement** in `cmd/wt/smoke.go`

```go
// smokeTarget is the resolved model to test, its catalog row (Action tells
// whether it must be started first), and the agents that can run it.
type smokeTarget struct {
	Row    catalog.Row
	Agents []string
}

// Start reports whether the model is idle and must be started before testing.
func (t smokeTarget) Start() bool { return t.Row.Action() == catalog.ActionStart }

func resolveSmokeModel(cfg *config.Config, theme themes.Theme, modelID string) (smokeTarget, error) {
	cands := smoke.Candidates(cfg)
	if modelID != "" {
		for _, c := range cands {
			if c.Row.Model.ID == modelID {
				return smokeTarget{Row: c.Row, Agents: c.Agents}, nil
			}
		}
		if config.IndexModelByID(cfg.Models, modelID) >= 0 {
			return smokeTarget{}, fmt.Errorf(
				"model %q cannot be smoke-tested right now (not exposed, not on disk, or no agent supports it — check `modelman litellm status`; a local model must exist on disk)", modelID)
		}
		return smokeTarget{}, fmt.Errorf("unknown model %q", modelID)
	}
	if len(cands) == 0 {
		return smokeTarget{}, fmt.Errorf("no models are available for any agent — expose a cloud model or pull a local one")
	}
	if !stdinTTY() {
		return smokeTarget{}, fmt.Errorf("wt smoke needs a TTY to list models; pass a model id directly (wt smoke <provider>/<name>)")
	}
	models := make([]config.Model, len(cands))
	for i, c := range cands {
		models[i] = c.Row.Model
	}
	m, ok, err := pickModelTUI(cfg, models, theme)
	if err != nil {
		return smokeTarget{}, err
	}
	if !ok {
		return smokeTarget{}, fmt.Errorf("model selection canceled")
	}
	for _, c := range cands {
		if c.Row.Model.ID == m.ID {
			return smokeTarget{Row: c.Row, Agents: c.Agents}, nil
		}
	}
	return smokeTarget{}, fmt.Errorf("unknown model %q", m.ID)
}

// smokeStopFlow is the exit flow: release this process's refcount entry, then
// offer to stop running local models nothing else uses. Skipped for --json and
// when stdin is not a TTY.
func smokeStopFlow(cfg *config.Config, jsonOut bool) {
	if jsonOut || !stdinTTY() {
		return
	}
	releaseSession()
	runStopPicker(cfg)
}
```

Move the body of `RunE` into `runSmoke` (returns `anyFail, err`): the only changes are (a) `m, eligible, err := resolveSmokeModel(...)` becomes `t, err := resolveSmokeModel(...)` with `m := t.Row.Model` and `eligible := t.Agents`; (b) right after successful resolution and the `--only`/timeout validation, add `defer smokeStopFlow(a.cfg, jsonOut)` (read `jsonOut` before the defer; move its `GetBool` above); (c) before the agent loop:

```go
if t.Start() {
	replace, _ := cmd.Flags().GetBool("replace")
	if err := startModel(a.cfg, t.Row, replace); err != nil {
		return false, err
	}
}
```
and the final `smokeExit(1)` moves out of `runSmoke` into `RunE`: 

```go
RunE: func(cmd *cobra.Command, args []string) error {
	anyFail, err := runSmoke(cmd, a, args)
	if err != nil {
		return err
	}
	if anyFail {
		smokeExit(1) // after runSmoke's deferred stop flow has already run
	}
	return nil
},
```
Update the command's `Long` text: "With no model-id, shows the full model picker (requires a TTY). A local model that isn't running is started first; on exit, offers to stop running local models." Update the doc comments on `resolveSmokeModel` accordingly. Add imports `catalog`. Remove the now-unused `smoke.Eligibility` call site if `go vet` flags an unused import.

- [ ] **Step 4: Run and commit**

Run: `go test ./cmd/wt ./internal/smoke 2>&1 | tail -20 && go vet ./... && gofmt -l cmd internal`
Expected: PASS.

```bash
git add cmd/wt/smoke.go cmd/wt/smoke_test.go internal/smoke/smoke.go
git commit -m "feat(wt): wt smoke starts idle local models and runs the stop flow on exit - completes plan item #5

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 6: `wt --help` and documentation

**Files:**
- Modify: `cmd/wt/main.go` (root `Example`)
- Test: `cmd/wt/main_test.go` (or `model_cmds_test.go`)
- Modify: `wt/CLAUDE.md`, `wt/docs/wt-smoke.md`
- Create: `wt/docs/wt-start-stop.md`
- Modify: the `docs/guides/` model guide that documents `modelman start/stop` (find with `git grep -ln "modelman start" ../docs/guides`) — add a one-line pointer to `wt start`/`wt stop`; then run `git grep -n "exposed = " ../docs/guides/` before/after to confirm no snapshot drift, and `make -C .. check-links`.

- [ ] **Step 1: Failing test**

```go
// TestRootHelpListsModelSubcommands verifies `wt --help` documents start, stop
// and smoke (with examples), so the new commands are discoverable without
// reading docs.
func TestRootHelpListsModelSubcommands(t *testing.T) {
	root := newRootCmd(&app{}) // use the constructor main_test.go already uses to build the root command
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start", "stop", "smoke", "wt start", "wt stop", "wt smoke"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q:\n%s", want, out.String())
		}
	}
}
```
(Find the actual root constructor name in `cmd/wt/main.go` — it is the function containing `cmd := &cobra.Command{Use: "wt", ...}`; use it and the way `main_test.go` builds an `app`.)

- [ ] **Step 2: Run** `go test ./cmd/wt -run TestRootHelpListsModelSubcommands -v` — Expected: FAIL only on the `wt start`/`wt stop`/`wt smoke` example strings (subcommand names already appear from cobra's command list).

- [ ] **Step 3: Implement.** Extend root `Example` with:

```
  wt start [model]             # start a local model (picker when omitted)
  wt stop [model|provider]     # stop a local model or provider (picker when omitted)
  wt smoke [model]             # smoke-test every agent that supports a model
```

- [ ] **Step 4: Docs.**
  - `wt/CLAUDE.md`: in "Smoke test" replace "never starts/stops local models" with the new behavior (starts an idle local pick first via `startModel`, runs the stop picker on exit unless `--json`/non-TTY); add a "Start/stop (`wt start`, `wt stop`)" section (argument forms, `--yes`, in-use confirmation, `survey.StopCandidates`/`PickerWith`/`StopEntries`); add `cmd/wt/model_cmds.go` to the path table; add `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `pickModelTUI` to the test-seam list; update the `internal/smoke` table row (`Candidates`); note the picker's blocked-row behavior in the `internal/tui` row.
  - `wt/docs/wt-smoke.md`: rewrite the "Eligibility" paragraph (start rows; blocked rows excluded), add an "Exit flow" paragraph, update the `model-id` bullet.
  - `wt/docs/wt-start-stop.md`: usage, arguments, exit codes, in-use confirmation, examples, and the two selection screens.
  - Guide pointer as noted above.

- [ ] **Step 5: Full verification and commit**

Run from repo root: `make test-all 2>&1 | tail -30` (or, if modelman deps are unavailable, `cd wt && go build ./... && go vet ./... && go test ./... && cd .. && make lint`).
Expected: all pass; `git grep -n "exposed = " docs/guides/` unchanged.

```bash
git add -A wt docs
git commit -m "docs(wt): help text and guides for start/stop/smoke - completes plan item #6

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

Then hand back for review (`superpowers:requesting-code-review`) and ask the user whether to create a PR — do not push.

---

## Self-Review

**Spec coverage:** start (with model, no model, running no-op, blocked, unknown/cloud) → Task 4; stop (model, provider, invalid, not running, in-use guard + `--yes`, no-arg picker incl. in-use, TTY errors) → Tasks 1–2; smoke (all rows, start first, blocked error, exit flow on pass/fail, skipped for `--json`/non-TTY, exit code preserved) → Tasks 3 and 5; help → Task 6; screen 1 sharing across `wt`/`start`/`smoke` (existing `wt` model step untouched; `PickModel` gains blocked-row handling) → Task 4; screen 2 exit flows keep hiding in-use → Task 1 (`stoppable` unchanged semantics); docs → Task 6.

**Deliberate refinements of the spec:** (1) `wt stop <provider>` with nothing running exits 0 with a note (idempotent) while `wt stop <model>` not running errors, as specified; (2) the smoke exit flow runs once a model has been resolved (so it covers start failures, PASS, FAIL and Ctrl+C mid-run) but not after an aborted/invalid selection where nothing could have started; (3) the in-use count for a provider-wide stop is the maximum across its models.

**Type consistency:** `survey.Candidate{Entry, Sessions}`, `StopCandidates`, `StopEntries(w, cfg, entries) error`, `PickerWith/Options` (Task 1) match their uses in Task 2; `smoke.Candidate{Row, Agents}` and `Candidates` (Task 3) match Task 5; `startModel(cfg, row, replace)`, `pickModelTUI`, `runStart/runStop` names are used consistently.
