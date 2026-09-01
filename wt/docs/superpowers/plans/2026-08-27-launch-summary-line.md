# Launch Summary Line Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Print a single `wt: <agent> · <model-id> · <duration>` line to stdout after every agent/command subprocess exits, on both success and failure.

**Architecture:** A shared `Summary` formatter in `internal/agents` (already imported by both launch paths) produces the line. Each launch path (`cmd/wt/launch.go` non-TUI, `internal/tui/launch.go` TUI) measures `time.Now()` around its existing `cmd.Run()` and prints the summary after the run returns. The TUI prints after `RestoreTerminal()` so the line lands on a clean line.

**Tech Stack:** Go 1.26, standard library only (`time`, `fmt`, `os`, `os/exec`).

**Spec:** `docs/superpowers/specs/2026-08-27-launch-summary-line-design.md`

---

## File Structure

- **Create** `internal/agents/summary.go` — the `Summary(agent string, m config.Model, d time.Duration) string` formatter.
- **Create** `internal/agents/summary_test.go` — unit tests for the formatter.
- **Modify** `cmd/wt/launch.go` — thread `agent`/`model` into `runAgentCmd`, measure time, print summary.
- **Modify** `cmd/wt/launch_test.go` — test that `runAgentCmd` prints the summary.
- **Modify** `internal/tui/launch.go` — thread `agent`/`model` into `runAndWaitCmd`, measure time, print summary.
- **Modify** `internal/tui/app.go` — update the two `runAndWaitCmd` call sites.
- **Modify** `internal/tui/launch_test.go` — test that `runAndWaitCmd` prints the summary (success + failure).
- **Modify** `internal/tui/launch_lifecycle_test.go` — update existing `runAndWaitCmd` callers for the new signature.

---

## Task 1: Add the `Summary` formatter

**Files:**
- Create: `internal/agents/summary.go`
- Test: `internal/agents/summary_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agents/summary_test.go`:

```go
package agents

import (
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestSummaryModelAgent asserts the full three-segment line for a model agent.
func TestSummaryModelAgent(t *testing.T) {
	got := Summary("claude", config.Model{ID: "claude/sonnet"}, 3*time.Minute+42*time.Second)
	want := "wt: claude · claude/sonnet · 3m42s"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestSummaryCommandAgentOmitsModel asserts command agents (empty model ID)
// drop the model segment.
func TestSummaryCommandAgentOmitsModel(t *testing.T) {
	got := Summary("shell", config.Model{}, 850*time.Millisecond)
	want := "wt: shell · 850ms"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestSummaryRoundsSubSecond asserts durations ≥1s round to seconds (1.2s → 1s).
func TestSummaryRoundsSubSecond(t *testing.T) {
	got := Summary("shell", config.Model{}, 1200*time.Millisecond)
	want := "wt: shell · 1s"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agents -run TestSummary -v`
Expected: FAIL with `undefined: Summary`

- [ ] **Step 3: Write the implementation**

Create `internal/agents/summary.go`:

```go
package agents

import (
	"fmt"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Summary formats the post-run summary line printed to stdout after an agent
// or command subprocess exits. Command agents (m.ID == "") omit the model
// segment. Durations ≥1s are rounded to seconds; shorter durations are shown
// in milliseconds.
func Summary(agent string, m config.Model, d time.Duration) string {
	if d >= time.Second {
		d = d.Round(time.Second)
	} else {
		d = d.Round(time.Millisecond)
	}
	if m.ID == "" {
		return fmt.Sprintf("wt: %s · %s", agent, d)
	}
	return fmt.Sprintf("wt: %s · %s · %s", agent, m.ID, d)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agents -run TestSummary -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agents/summary.go internal/agents/summary_test.go
git commit -m "feat(agents): add post-run Summary formatter"
```

---

## Task 2: Non-TUI path prints the summary

**Files:**
- Modify: `cmd/wt/launch.go` (the `runAgentCmd` function and its two call sites in `launchFilteredImpl`)
- Test: `cmd/wt/launch_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/wt/launch_test.go` (add `"io"` to the import block):

```go
// TestRunAgentCmdPrintsSummary asserts the non-TUI launch path prints the
// summary line to stdout after the subprocess exits.
func TestRunAgentCmdPrintsSummary(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("stdout = %q, want summary line", string(out))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wt -run TestRunAgentCmdPrintsSummary -v`
Expected: FAIL — `runAgentCmd` takes one argument, not three (compile error).

- [ ] **Step 3: Write the implementation**

In `cmd/wt/launch.go`, add `"time"` to the import block, then replace `runAgentCmd`:

```go
// runAgentCmd wires stdio through to the agent, runs it, prints the post-run
// summary line, and propagates the agent's exit code to the caller.
func runAgentCmd(cmd *exec.Cmd, agent string, m config.Model) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	err := cmd.Run()
	fmt.Println(agents.Summary(agent, m, time.Since(start)))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
```

Update the two call sites in `launchFilteredImpl`:

The command-agent branch:

```go
		return runAgentCmd(cmd, agent, config.Model{})
```

The model branch (the final `return` of `launchFilteredImpl`):

```go
	return runAgentCmd(cmd, agent, m)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wt -run TestRunAgentCmdPrintsSummary -v`
Expected: PASS

- [ ] **Step 5: Run the full package to catch regressions**

Run: `go test ./cmd/wt ./internal/agents`
Expected: PASS (existing `launchFiltered` tests exercise the new signature through the wired path)

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "feat(wt): print post-run summary line on non-TUI launch"
```

---

## Task 3: TUI path prints the summary

**Files:**
- Modify: `internal/tui/launch.go` (`runAndWaitCmd`)
- Modify: `internal/tui/app.go` (`launchAndRecord` and `launchCommand` call sites)
- Modify: `internal/tui/launch_lifecycle_test.go` (existing `runAndWaitCmd` callers)
- Test: `internal/tui/launch_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/launch_test.go` (add `"io"` to the import block):

```go
// TestRunAndWaitCmdPrintsSummaryOnSuccess asserts the TUI launch path prints
// the summary line to stdout after a successful subprocess exit.
func TestRunAndWaitCmdPrintsSummaryOnSuccess(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"})()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("stdout = %q, want summary line", string(out))
	}
}

// TestRunAndWaitCmdPrintsSummaryOnFailure asserts the summary line is printed
// even when the subprocess exits non-zero.
func TestRunAndWaitCmdPrintsSummaryOnFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("`false` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(falsePath)
	msg := runAndWaitCmd(cmd, "shell", config.Model{})()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("expected error from `false`")
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "wt: shell ·") {
		t.Errorf("stdout = %q, want summary line", string(out))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run TestRunAndWaitCmdPrintsSummary -v`
Expected: FAIL — `runAndWaitCmd` takes one argument, not three (compile error).

- [ ] **Step 3: Write the implementation**

In `internal/tui/launch.go`, add `"time"` to the import block, then replace `runAndWaitCmd`:

```go
// runAndWaitCmd releases the TUI, runs the agent with stdio wired to the
// terminal, restores the TUI, prints the post-run summary line, and returns a
// launchDoneMsg.
func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		err := cmd.Run()
		if currentProgram != nil {
			currentProgram.RestoreTerminal()
		}
		fmt.Println(agents.Summary(agent, m, time.Since(start)))
		return launchDoneMsg{err: err}
	}
}
```

In `internal/tui/app.go`, update the two call sites:

`launchAndRecord` (model path):

```go
	return m, runAndWaitCmd(cmd, m.agent, m.launchModel)
```

`launchCommand` (command path):

```go
	return m, runAndWaitCmd(cmd, name, config.Model{})
```

- [ ] **Step 4: Update existing `runAndWaitCmd` callers in tests**

In `internal/tui/launch_lifecycle_test.go`, update three call sites to pass `"shell", config.Model{}`:

- `TestRunAndWaitCmdBlocksUntilAgentExits`: `runAndWaitCmd(cmd)()` → `runAndWaitCmd(cmd, "shell", config.Model{})()`
- `TestRunAndWaitCmdReturnsErrorOnFailure`: `runAndWaitCmd(cmd)()` → `runAndWaitCmd(cmd, "shell", config.Model{})()`
- `TestEnterInModelPhaseDoesNotQuitImmediately`: `runAndWaitCmd(agentCmd)` → `runAndWaitCmd(agentCmd, "shell", config.Model{})`

In `internal/tui/launch_test.go`, update:

- `TestRunAndWaitCmdWiresStdio`: `runAndWaitCmd(cmd)()` → `runAndWaitCmd(cmd, "shell", config.Model{})()`

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestRunAndWaitCmd|TestEnterInModelPhase' -v`
Expected: PASS

- [ ] **Step 6: Run the full package to catch regressions**

Run: `go test ./internal/tui`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/tui/launch.go internal/tui/app.go internal/tui/launch_test.go internal/tui/launch_lifecycle_test.go
git commit -m "feat(tui): print post-run summary line on TUI launch"
```

---

## Task 4: Full verification

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 2: Static analysis and build**

Run: `go vet ./... && go build ./...`
Expected: no output, exit 0

- [ ] **Step 3: Manual smoke check (optional, needs a TTY)**

Run: `wt --cwd -A shell -- echo hi`
Expected: the command runs, then a line like `wt: shell · 0s` prints to stdout.
