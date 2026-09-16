# `wt smoke` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `wt smoke <model-id>` command that finds every agent currently eligible for a model and runs a one-shot prompt through each, in-process, reporting PASS/FAIL/SKIP.

**Architecture:** A new `OneShotRunner` optional driver capability (`internal/agents`) teaches six drivers how to run non-interactively. A new `internal/smoke` package computes eligibility (reusing `Config.EligibleModels` + `internal/localgate.Apply`, the same rules a real launch applies) and runs/classifies rows through a swappable `buildAndRun` seam, so classification logic is unit-testable with no real subprocess exec. `cmd/wt/smoke.go` is a thin cobra command: resolve the model (pinned or interactive pick), call `internal/smoke`, render human or `--json` output, set the process exit code.

**Tech Stack:** Go 1.26.7, cobra (existing dependency), stdlib only otherwise (no new third-party deps).

**Spec:** `wt/docs/superpowers/specs/2026-09-16-wt-smoke-design.md`

## Global Constraints

- Module root is `wt/` — run `go build ./...` / `go test ./...` / `go vet ./...` from there, never the monorepo root.
- Strictly read-only against modelman-owned state (`registry.toml`, `modelman.toml`): no writes, no LiteLLM routing-mode flips, no provider start/stop.
- No live-binary test is added to `go test ./...` / `make test`. All process execution goes through the `buildAndRun` seam in tests.
- Default `--timeout`: `180s` (matches `agents-smoke.sh`).
- Default prompt: `Reply with exactly this text and nothing else: WT-SMOKE-<agent>-<runid>`. PASS requires exit code 0 AND the sentinel present in captured output. A `--prompt` override degrades verification to exit-code-0-only (documented, not silent).
- Exit code: `0` if every non-SKIP row PASSed, `1` if any row FAILed. SKIP (agent binary not installed) never affects the exit code.
- Every `Test*` gets a top-level `//` comment stating what it tests and why it matters (repo convention, `wt/CLAUDE.md`'s Go Tests section).
- New seams follow the existing `var x = realX` shape (`wt/CLAUDE.md`'s Go Tests section).

---

## Task 1: `OneShotRunner` driver capability

**Files:**
- Modify: `internal/agents/agents.go` (add the `OneShotRunner` interface)
- Modify: `internal/agents/claude.go`, `codex.go`, `copilot.go`, `opencode.go`, `pi.go`, `agy.go` (each gains one method)
- Test: `internal/agents/oneshot_test.go` (new file)

**Interfaces:**
- Produces: `OneShotRunner` interface (`OneShotArgs(prompt string) []string`), implemented by `claudeDriver`, `codexDriver`, `copilotDriver`, `opencodeDriver`, `piDriver`, `agyDriver`. `shellDriver` does not implement it. Consumed by Task 2's `internal/smoke.realBuildAndRun`.

- [ ] **Step 1: Write the failing test**

Create `internal/agents/oneshot_test.go`:

```go
package agents

import (
	"slices"
	"testing"
)

// TestOneShotArgs asserts each one-shot-capable driver returns the exact
// non-interactive invocation flags wt smoke depends on to run a single
// prompt and exit — a wrong flag here silently breaks wt smoke for that
// agent without any test ever executing a real binary.
func TestOneShotArgs(t *testing.T) {
	tests := []struct {
		agent string
		want  []string
	}{
		{"claude", []string{"-p", "hello"}},
		{"codex", []string{"exec", "hello"}},
		{"copilot", []string{"-p", "hello"}},
		{"opencode", []string{"run", "hello"}},
		{"pi", []string{"-p", "hello"}},
		{"agy", []string{"-p", "hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			d := ByName(tt.agent)
			if d == nil {
				t.Fatalf("unknown agent %q", tt.agent)
			}
			osr, ok := d.(OneShotRunner)
			if !ok {
				t.Fatalf("driver %q does not implement OneShotRunner", tt.agent)
			}
			got := osr.OneShotArgs("hello")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("OneShotArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShellNotOneShotRunner asserts shell — a command with no model layer —
// does not implement OneShotRunner. wt smoke's eligibility filter relies on
// agents.IsCommand to exclude shell, not on a missing method; this test
// guards against the two mechanisms silently diverging.
func TestShellNotOneShotRunner(t *testing.T) {
	d := ByName("shell")
	if d == nil {
		t.Fatal(`unknown agent "shell"`)
	}
	if _, ok := d.(OneShotRunner); ok {
		t.Fatal("shellDriver unexpectedly implements OneShotRunner")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agents -run 'TestOneShotArgs|TestShellNotOneShotRunner' -v`
Expected: compile failure — `OneShotRunner` is undefined.

- [ ] **Step 3: Add the `OneShotRunner` interface**

In `internal/agents/agents.go`, add this after the `Resumer` interface block (after line 102, before the `InstructionPointer` type):

```go
// OneShotRunner is an optional Driver capability for agents that can run a
// single prompt non-interactively and exit. wt smoke appends OneShotArgs'
// return value to the command Build already constructed, to verify a
// model works through this agent without an interactive session.
type OneShotRunner interface {
	OneShotArgs(prompt string) []string
}
```

- [ ] **Step 4: Implement `OneShotArgs` on the six drivers**

Append to `internal/agents/claude.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (claudeDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
```

Append to `internal/agents/codex.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (codexDriver) OneShotArgs(prompt string) []string { return []string{"exec", prompt} }
```

Append to `internal/agents/copilot.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (copilotDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
```

Append to `internal/agents/opencode.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (opencodeDriver) OneShotArgs(prompt string) []string { return []string{"run", prompt} }
```

Append to `internal/agents/pi.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (piDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
```

Append to `internal/agents/agy.go`:

```go
// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (agyDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/agents -run 'TestOneShotArgs|TestShellNotOneShotRunner' -v`
Expected: PASS (7 subtests: 6 agents + `TestShellNotOneShotRunner`).

- [ ] **Step 6: Run the full package suite and vet**

Run: `go test ./internal/agents/... && go vet ./internal/agents/...`
Expected: all existing tests still pass; no vet issues.

- [ ] **Step 7: Commit**

```bash
git add internal/agents/agents.go internal/agents/claude.go internal/agents/codex.go internal/agents/copilot.go internal/agents/opencode.go internal/agents/pi.go internal/agents/agy.go internal/agents/oneshot_test.go
git commit -m "wt: add OneShotRunner driver capability for one-shot invocation

Teaches claude, codex, copilot, opencode, pi, and agy how to run a
single prompt non-interactively and exit, mirroring the flags
agents-smoke.sh's bash MATRIX already hand-encodes. shell (a
command, no model layer) does not implement it. Lays the groundwork
for wt smoke - completes plan item #1"
```

---

## Task 2: `internal/smoke` package — eligibility, execution, classification

**Files:**
- Create: `internal/smoke/smoke.go`
- Test: `internal/smoke/smoke_test.go`

**Interfaces:**
- Consumes: `agents.IsCommand(name string) bool`, `agents.ByName(name string) Driver`, `agents.OneShotRunner`, `agents.BuildLaunchCmd(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error)` (Task 1 + existing); `config.Config.EligibleModels(agentName, tags, family string) ([]config.Model, error)`, `config.IndexModelByID(models []config.Model, id string) int`, `localgate.Apply(cfg *config.Config, models []config.Model, pinned string) localgate.Result` (existing).
- Produces: `smoke.Status` (`StatusPass`, `StatusFail`, `StatusSkip`), `smoke.RowResult{Agent, Model, Status, ExitCode, Duration, Command, Output, Err}`, `smoke.EligibleAgents(cfg *config.Config, m config.Model) []string`, `smoke.AllEligibleModels(cfg *config.Config) []config.Model`, `smoke.RunRow(cfg *config.Config, agentName string, m config.Model, prompt, sentinel string, timeout time.Duration, cwd string) RowResult`, `smoke.NewRunID() string`, `smoke.DefaultPrompt(agentName, runID string) (prompt, sentinel string)` — all consumed by Task 3's `cmd/wt/smoke.go`.

- [ ] **Step 1: Write the failing tests**

Create `internal/smoke/smoke_test.go`:

```go
// Tests for internal/smoke's eligibility computation and row classification.
// Process execution is stubbed via the buildAndRun seam throughout — no
// real subprocess is ever exec'd here.
package smoke

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// smokeFixtureConfig builds a small multi-agent, multi-provider config:
// claude supports ollama+openrouter, codex supports only ollama, agy only
// its own native provider, and shell is deliberately included as an Agent
// entry (with a provider it would otherwise match) to prove EligibleAgents
// excludes it via IsCommand rather than relying on it being absent from
// config.toml.
func smokeFixtureConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "agy", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx", Location: config.LocationLocal},
			{ID: "openrouter/glm-5.3-flash", ProviderID: "openrouter", ModelName: "glm-5.3-flash", Location: config.LocationCloud},
			{ID: "agy/native", ProviderID: "agy", ModelName: "native", Native: true, Location: config.LocationCloud},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama", "openrouter"}},
			{Name: "codex", SupportedProviders: []string{"ollama"}},
			{Name: "agy", SupportedProviders: []string{"agy"}},
			{Name: "shell", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("ollama/qwen3.8:27b-mlx")
	return cfg
}

func findModel(t *testing.T, cfg *config.Config, id string) config.Model {
	t.Helper()
	idx := config.IndexModelByID(cfg.Models, id)
	if idx < 0 {
		t.Fatalf("fixture missing model %q", id)
	}
	return cfg.Models[idx]
}

// TestEligibleAgentsExcludesShellAndMatchesProviders asserts a local model
// is eligible only for agents whose supported_providers include its
// provider, and that shell is excluded even though it's configured with a
// matching provider in this fixture — proving the exclusion comes from
// agents.IsCommand, not from shell simply being absent.
func TestEligibleAgentsExcludesShellAndMatchesProviders(t *testing.T) {
	cfg := smokeFixtureConfig()
	m := findModel(t, cfg, "ollama/qwen3.8:27b-mlx")
	got := EligibleAgents(cfg, m)
	want := []string{"claude", "codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestEligibleAgentsAgyNarrowedToNative asserts agy — whose only supported
// provider is its own native one — is eligible only for agy/native, never
// for a model under another provider, mirroring agents-smoke.sh's comment
// that agy's driver ignores whatever model is passed.
func TestEligibleAgentsAgyNarrowedToNative(t *testing.T) {
	cfg := smokeFixtureConfig()
	m := findModel(t, cfg, "agy/native")
	got := EligibleAgents(cfg, m)
	want := []string{"agy"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestEligibleAgentsCloudModel asserts a cloud model is only eligible for
// the agent(s) whose supported_providers list that provider.
func TestEligibleAgentsCloudModel(t *testing.T) {
	cfg := smokeFixtureConfig()
	m := findModel(t, cfg, "openrouter/glm-5.3-flash")
	got := EligibleAgents(cfg, m)
	want := []string{"claude"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestAllEligibleModelsUnionsAcrossAgents asserts the interactive picker's
// candidate list is the union (deduped, sorted by id) of every model
// eligible for at least one non-command agent.
func TestAllEligibleModelsUnionsAcrossAgents(t *testing.T) {
	cfg := smokeFixtureConfig()
	got := AllEligibleModels(cfg)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	want := []string{"agy/native", "ollama/qwen3.8:27b-mlx", "openrouter/glm-5.3-flash"}
	if !slices.Equal(ids, want) {
		t.Fatalf("AllEligibleModels ids = %v, want %v", ids, want)
	}
}

// withStubBuildAndRun replaces the buildAndRun seam for the duration of a
// test, restoring the original on cleanup.
func withStubBuildAndRun(t *testing.T, out execOutcome) {
	t.Helper()
	old := buildAndRun
	buildAndRun = func(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration) execOutcome {
		return out
	}
	t.Cleanup(func() { buildAndRun = old })
}

// TestRunRowPass asserts exit 0 plus the sentinel present in captured
// output classifies PASS.
func TestRunRowPass(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "before WT-SMOKE-claude-run-1 after"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (err=%v)", res.Status, res.Err)
	}
}

// TestRunRowFailMissingSentinel asserts exit 0 without the sentinel in
// output classifies FAIL, not PASS — a process exiting cleanly is not
// enough on its own when a sentinel was requested.
func TestRunRowFailMissingSentinel(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "wrong output"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailNonZeroExit asserts a non-zero exit code is FAIL
// regardless of what the output contains.
func TestRunRowFailNonZeroExit(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 1, Output: "WT-SMOKE-claude-run-1"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowSkipNotInstalled asserts a StartErr matching agents.Command's
// exact "agent <name> not installed" text classifies SKIP — the same
// substring convention agents-smoke.sh's classifier uses.
func TestRunRowSkipNotInstalled(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf("agent claude not installed")})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusSkip {
		t.Fatalf("Status = %v, want SKIP", res.Status)
	}
}

// TestRunRowFailOtherStartErr asserts a StartErr that is NOT the
// not-installed message (e.g. a route-resolution failure) classifies FAIL,
// not SKIP — only the exact installed-check message means SKIP.
func TestRunRowFailOtherStartErr(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf(`unknown provider "x" for model "y"`)})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailTimeout asserts a timed-out row classifies FAIL with a
// timeout-specific error, and never blocks on a hung process.
func TestRunRowFailTimeout(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{TimedOut: true})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusFail || res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("Status = %v, Err = %v, want FAIL with a timeout error", res.Status, res.Err)
	}
}

// TestRunRowCustomPromptSkipsSentinelCheck asserts an empty sentinel (the
// --prompt override case) makes exit-code-0 alone sufficient for PASS —
// the documented verification degradation for a custom prompt.
func TestRunRowCustomPromptSkipsSentinelCheck(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "anything at all"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "do the task", "", time.Second, ".")
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (empty sentinel means exit-code-only)", res.Status)
	}
}

// TestNewRunIDFormat asserts NewRunID produces the "run-<8 hex chars>"
// shape RunRow's default prompt embeds.
func TestNewRunIDFormat(t *testing.T) {
	id := NewRunID()
	if !strings.HasPrefix(id, "run-") {
		t.Fatalf("NewRunID() = %q, want run- prefix", id)
	}
	suffix := strings.TrimPrefix(id, "run-")
	if len(suffix) != 8 {
		t.Fatalf("NewRunID() suffix = %q, want 8 hex chars", suffix)
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("NewRunID() = %q contains non-hex character %q", id, r)
		}
	}
}

// TestDefaultPrompt asserts the sentinel is embedded verbatim in the
// prompt RunRow will later search for in captured output.
func TestDefaultPrompt(t *testing.T) {
	prompt, sentinel := DefaultPrompt("claude", "run-abcd1234")
	wantSentinel := "WT-SMOKE-claude-run-abcd1234"
	if sentinel != wantSentinel {
		t.Fatalf("sentinel = %q, want %q", sentinel, wantSentinel)
	}
	if !strings.Contains(prompt, sentinel) {
		t.Fatalf("prompt %q does not contain sentinel %q", prompt, sentinel)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/smoke/... -v`
Expected: compile failure — package `internal/smoke` does not exist yet.

- [ ] **Step 3: Implement `internal/smoke/smoke.go`**

```go
// Package smoke implements wt smoke's testable core: given a model, find
// every currently eligible agent and run a one-shot prompt through each,
// classifying the result. Process execution goes through the buildAndRun
// seam so tests can verify classification logic (PASS/FAIL/SKIP, sentinel
// matching, timeouts) without executing real agent binaries.
package smoke

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
)

// Status is a row's classification.
type Status string

const (
	StatusPass Status = "PASS"
	StatusFail Status = "FAIL"
	StatusSkip Status = "SKIP"
)

// RowResult is one agent's outcome for the model under test.
type RowResult struct {
	Agent    string
	Model    string
	Status   Status
	ExitCode int
	Duration time.Duration
	Command  string
	Output   string
	Err      error
}

// EligibleAgents returns, sorted, every non-command agent currently
// eligible to launch model m — the same membership (provider support) and
// exposure/local-running-gate rules wt's own launch path applies via
// Config.EligibleModels and localgate.Apply, so a model wt smoke reports
// eligible is a model a real `wt -A <agent> -M <id>` launch would accept.
func EligibleAgents(cfg *config.Config, m config.Model) []string {
	var out []string
	for _, a := range cfg.Agents {
		if agents.IsCommand(a.Name) {
			continue
		}
		eligible, err := cfg.EligibleModels(a.Name, "", "")
		if err != nil {
			continue
		}
		gate := localgate.Apply(cfg, eligible, "")
		if config.IndexModelByID(gate.Eligible, m.ID) >= 0 {
			out = append(out, a.Name)
		}
	}
	sort.Strings(out)
	return out
}

// AllEligibleModels returns, sorted by id, the union of every model
// currently eligible for at least one non-command agent — the candidate
// list wt smoke's interactive picker offers when no model id is given.
func AllEligibleModels(cfg *config.Config) []config.Model {
	seen := map[string]bool{}
	var out []config.Model
	for _, a := range cfg.Agents {
		if agents.IsCommand(a.Name) {
			continue
		}
		eligible, err := cfg.EligibleModels(a.Name, "", "")
		if err != nil {
			continue
		}
		gate := localgate.Apply(cfg, eligible, "")
		for _, m := range gate.Eligible {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// execOutcome is what the buildAndRun seam reports for one row.
type execOutcome struct {
	Command  string
	Output   string
	ExitCode int
	TimedOut bool
	StartErr error // non-nil if the command never started (e.g. agent not installed)
}

// buildAndRun builds and runs one agent's one-shot launch command. It is a
// package-level var so tests can stub it with canned outcomes, following
// the seam pattern documented in wt/CLAUDE.md's Go Tests section.
var buildAndRun = realBuildAndRun

// notInstalledMessage mirrors agents.Command's exact error text ("agent %s
// not installed") so RunRow can classify a missing binary as SKIP rather
// than FAIL. Matches the same substring convention agents-smoke.sh uses.
func notInstalledMessage(agent string) string {
	return fmt.Sprintf("agent %s not installed", agent)
}

func realBuildAndRun(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration) execOutcome {
	cmd, err := agents.BuildLaunchCmd(agentName, m, cwd, false, nil, cfg, nil)
	if err != nil {
		return execOutcome{StartErr: err}
	}
	d := agents.ByName(agentName)
	osr, ok := d.(agents.OneShotRunner)
	if !ok {
		return execOutcome{StartErr: fmt.Errorf("agent %q does not support one-shot invocation", agentName)}
	}
	cmd.Args = append(cmd.Args, osr.OneShotArgs(prompt)...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmdLine := strings.Join(cmd.Args, " ")

	if err := cmd.Start(); err != nil {
		return execOutcome{Command: cmdLine, StartErr: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		exitCode := 0
		if waitErr != nil {
			ee, ok := waitErr.(*exec.ExitError)
			if !ok {
				return execOutcome{Command: cmdLine, Output: buf.String(), StartErr: waitErr}
			}
			exitCode = ee.ExitCode()
		}
		return execOutcome{Command: cmdLine, Output: buf.String(), ExitCode: exitCode}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return execOutcome{Command: cmdLine, Output: buf.String(), TimedOut: true}
	}
}

// RunRow runs agentName's one-shot prompt against model m and classifies
// the result. sentinel is the exact string the default prompt asked the
// agent to echo; pass "" when prompt was overridden by the caller (a
// custom prompt was never asked to produce a sentinel), which degrades
// verification to "exited 0 within timeout".
func RunRow(cfg *config.Config, agentName string, m config.Model, prompt, sentinel string, timeout time.Duration, cwd string) RowResult {
	start := time.Now()
	out := buildAndRun(cfg, agentName, m, prompt, cwd, timeout)
	res := RowResult{
		Agent:    agentName,
		Model:    m.ID,
		Duration: time.Since(start),
		Command:  out.Command,
		Output:   out.Output,
		ExitCode: out.ExitCode,
	}

	if out.StartErr != nil {
		res.Err = out.StartErr
		if strings.Contains(out.StartErr.Error(), notInstalledMessage(agentName)) {
			res.Status = StatusSkip
		} else {
			res.Status = StatusFail
		}
		return res
	}
	if out.TimedOut {
		res.Status = StatusFail
		res.Err = fmt.Errorf("timed out after %s", timeout)
		return res
	}
	if out.ExitCode != 0 {
		res.Status = StatusFail
		res.Err = fmt.Errorf("exit code %d", out.ExitCode)
		return res
	}
	if sentinel != "" && !strings.Contains(out.Output, sentinel) {
		res.Status = StatusFail
		res.Err = fmt.Errorf("sentinel %q not found in output", sentinel)
		return res
	}
	res.Status = StatusPass
	return res
}

// NewRunID generates a run id for correlating one wt smoke invocation's
// rows. Hex-encoded (letters interleaved among digits), not a pure decimal
// run, so it never reads as a phone-number-shaped sequence pi's privacy
// filter redacts — mirroring agents-smoke.sh's md5-based id for the same
// reason.
func NewRunID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("run-%x", b)
}

// DefaultPrompt returns the sentinel-echo prompt and the sentinel RunRow
// should look for in the agent's output.
func DefaultPrompt(agentName, runID string) (prompt, sentinel string) {
	sentinel = fmt.Sprintf("WT-SMOKE-%s-%s", agentName, runID)
	prompt = fmt.Sprintf("Reply with exactly this text and nothing else: %s", sentinel)
	return prompt, sentinel
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/smoke/... -v`
Expected: PASS for all tests listed in Step 1.

- [ ] **Step 5: Run vet and the full module test suite**

Run: `go vet ./internal/smoke/... && go build ./...`
Expected: no vet issues; the whole module still builds (confirms no import cycle between `internal/smoke` and `internal/agents`/`internal/config`/`internal/localgate`).

- [ ] **Step 6: Commit**

```bash
git add internal/smoke/smoke.go internal/smoke/smoke_test.go
git commit -m "wt: add internal/smoke eligibility + row execution core

EligibleAgents/AllEligibleModels reuse Config.EligibleModels and
localgate.Apply so a model wt smoke reports eligible is exactly what
a real launch would accept. RunRow classifies PASS/FAIL/SKIP through
a swappable buildAndRun seam - completes plan item #2"
```

---

## Task 3: `cmd/wt/smoke.go` — CLI command

**Files:**
- Create: `cmd/wt/smoke.go`
- Test: `cmd/wt/smoke_test.go`

**Interfaces:**
- Consumes: `smoke.EligibleAgents`, `smoke.AllEligibleModels`, `smoke.RunRow`, `smoke.NewRunID`, `smoke.DefaultPrompt`, `smoke.RowResult`, `smoke.Status`/`StatusPass`/`StatusFail`/`StatusSkip` (Task 2); `config.IndexModelByID`, `config.ParseFilterList` (existing); `mustGetString`, `stdinTTY` (existing seam in `cmd/wt/helpers.go`); `app{cfg, cfgErr, theme}` (existing, `cmd/wt/app.go`).
- Produces: `smokeCmd(a *app) *cobra.Command`, consumed by Task 4's `rootCmd()` registration.

- [ ] **Step 1: Write the failing tests**

Create `cmd/wt/smoke_test.go`:

```go
// Tests for `wt smoke`'s CLI-layer logic: model resolution, --only
// filtering, the interactive picker, and human/JSON rendering. Row
// execution itself is internal/smoke's responsibility (tested there via
// the buildAndRun seam) — these tests work entirely with synthetic
// smoke.RowResult values, no real subprocess exec.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
)

// smokeFixtureConfig builds a one-agent, two-model config: one model is
// exposed and running (eligible), the other is registered but never
// exposed/running (not eligible) — enough to exercise resolveSmokeModel's
// three outcomes (eligible / registered-but-ineligible / unknown).
func smokeFixtureConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx", Location: config.LocationLocal},
			{ID: "ollama/not-eligible:x", ProviderID: "ollama", ModelName: "not-eligible:x", Location: config.LocationLocal},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("ollama/qwen3.8:27b-mlx") // "not-eligible" stays flagged off
	return cfg
}

// TestResolveSmokeModelPinnedEligible asserts a pinned id already in the
// eligible set resolves directly, with no TTY/picker interaction.
func TestResolveSmokeModelPinnedEligible(t *testing.T) {
	cfg := smokeFixtureConfig()
	m, err := resolveSmokeModel(cfg, "ollama/qwen3.8:27b-mlx")
	if err != nil {
		t.Fatalf("resolveSmokeModel: %v", err)
	}
	if m.ID != "ollama/qwen3.8:27b-mlx" {
		t.Fatalf("got model %q", m.ID)
	}
}

// TestResolveSmokeModelPinnedNotEligible asserts a pinned id that exists in
// the registry but isn't currently eligible (a local model that isn't
// running) gets a specific "not eligible" message, distinct from
// "unknown model".
func TestResolveSmokeModelPinnedNotEligible(t *testing.T) {
	cfg := smokeFixtureConfig()
	_, err := resolveSmokeModel(cfg, "ollama/not-eligible:x")
	if err == nil || !strings.Contains(err.Error(), "not currently eligible") {
		t.Fatalf("err = %v, want a not-currently-eligible message", err)
	}
}

// TestResolveSmokeModelUnknown asserts a pinned id absent from the
// registry entirely gets "unknown model".
func TestResolveSmokeModelUnknown(t *testing.T) {
	cfg := smokeFixtureConfig()
	_, err := resolveSmokeModel(cfg, "ollama/does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v, want unknown model error", err)
	}
}

// TestResolveSmokeModelNoModelNoTTY asserts omitting the model id without a
// TTY fails clearly, pointing at passing a model id directly, instead of
// trying to open a picker and failing opaquely.
func TestResolveSmokeModelNoModelNoTTY(t *testing.T) {
	old := stdinTTY
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = old }()

	cfg := smokeFixtureConfig()
	_, err := resolveSmokeModel(cfg, "")
	if err == nil || !strings.Contains(err.Error(), "needs a TTY") {
		t.Fatalf("err = %v, want a TTY-required message", err)
	}
}

// TestResolveSmokeModelNoneEligible asserts an empty eligible set errors
// with actionable guidance instead of silently opening an empty picker.
func TestResolveSmokeModelNoneEligible(t *testing.T) {
	cfg := &config.Config{}
	_, err := resolveSmokeModel(cfg, "")
	if err == nil || !strings.Contains(err.Error(), "no models are currently eligible") {
		t.Fatalf("err = %v, want a no-eligible-models message", err)
	}
}

// TestPickModelInteractiveValidSelection asserts a numbered choice resolves
// to the corresponding model and the listing is printed to w.
func TestPickModelInteractiveValidSelection(t *testing.T) {
	models := []config.Model{{ID: "a/1"}, {ID: "b/2"}}
	var out bytes.Buffer
	m, err := pickModelInteractive(strings.NewReader("2\n"), &out, models)
	if err != nil {
		t.Fatalf("pickModelInteractive: %v", err)
	}
	if m.ID != "b/2" {
		t.Fatalf("got %q, want b/2", m.ID)
	}
	if !strings.Contains(out.String(), "1) a/1") || !strings.Contains(out.String(), "2) b/2") {
		t.Fatalf("listing output missing entries: %q", out.String())
	}
}

// TestPickModelInteractiveOutOfRange asserts a selection outside the
// listed range is rejected rather than panicking on an out-of-bounds index.
func TestPickModelInteractiveOutOfRange(t *testing.T) {
	models := []config.Model{{ID: "a/1"}}
	_, err := pickModelInteractive(strings.NewReader("5\n"), &bytes.Buffer{}, models)
	if err == nil || !strings.Contains(err.Error(), "invalid selection") {
		t.Fatalf("err = %v, want invalid selection error", err)
	}
}

// TestFilterOnlyAgentsEmpty asserts an empty --only returns the eligible
// list unchanged.
func TestFilterOnlyAgentsEmpty(t *testing.T) {
	got, err := filterOnlyAgents([]string{"claude", "codex"}, "")
	if err != nil {
		t.Fatalf("filterOnlyAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want unchanged 2-element list", got)
	}
}

// TestFilterOnlyAgentsSubset asserts a valid subset is returned as given.
func TestFilterOnlyAgentsSubset(t *testing.T) {
	got, err := filterOnlyAgents([]string{"claude", "codex"}, "codex")
	if err != nil {
		t.Fatalf("filterOnlyAgents: %v", err)
	}
	if len(got) != 1 || got[0] != "codex" {
		t.Fatalf("got %v, want [codex]", got)
	}
}

// TestFilterOnlyAgentsUnknown asserts an agent outside the eligible set
// fails fast, before any row would run.
func TestFilterOnlyAgentsUnknown(t *testing.T) {
	_, err := filterOnlyAgents([]string{"claude"}, "codex")
	if err == nil || !strings.Contains(err.Error(), "not eligible for this model") {
		t.Fatalf("err = %v, want not-eligible error", err)
	}
}

// TestPrintSmokeHumanReportsFailAndDetail asserts a FAIL row gets an
// expanded detail block while PASS/SKIP stay single-line, and the
// function reports fail=true so the caller can set a non-zero exit code.
func TestPrintSmokeHumanReportsFailAndDetail(t *testing.T) {
	rows := []smoke.RowResult{
		{Agent: "claude", Status: smoke.StatusPass, Duration: 2 * time.Second},
		{Agent: "codex", Status: smoke.StatusFail, Duration: time.Second, Command: "codex exec ...", ExitCode: 1, Output: "boom"},
		{Agent: "copilot", Status: smoke.StatusSkip, Duration: 0},
	}
	var buf bytes.Buffer
	fail, err := printSmokeHuman(&buf, "run-1", "ollama/x", rows)
	if err != nil {
		t.Fatalf("printSmokeHuman: %v", err)
	}
	if !fail {
		t.Fatal("fail = false, want true (one FAIL row)")
	}
	out := buf.String()
	if !strings.Contains(out, "[FAIL ] codex") || !strings.Contains(out, "boom") {
		t.Fatalf("output missing FAIL detail: %q", out)
	}
	if !strings.Contains(out, "PASS: 1 FAIL: 1 SKIP: 1") {
		t.Fatalf("output missing summary counts: %q", out)
	}
}

// TestPrintSmokeJSONSchema asserts --json emits the documented contract
// (run_id/model/rows) and reports fail correctly for downstream callers.
func TestPrintSmokeJSONSchema(t *testing.T) {
	rows := []smoke.RowResult{
		{Agent: "claude", Model: "ollama/x", Status: smoke.StatusPass, Duration: 1500 * time.Millisecond},
	}
	var buf bytes.Buffer
	fail, err := printSmokeJSON(&buf, "run-1", "ollama/x", rows)
	if err != nil {
		t.Fatalf("printSmokeJSON: %v", err)
	}
	if fail {
		t.Fatal("fail = true, want false (no FAIL rows)")
	}
	var report smokeJSONReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, buf.String())
	}
	if report.RunID != "run-1" || report.Model != "ollama/x" || len(report.Rows) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Rows[0].DurationMS != 1500 {
		t.Fatalf("DurationMS = %d, want 1500", report.Rows[0].DurationMS)
	}
}

// TestSmokeCmdFlags asserts the command's flags are registered with the
// documented defaults — a cheap guard against a typo'd flag name/default
// silently breaking --timeout or --json.
func TestSmokeCmdFlags(t *testing.T) {
	cmd := smokeCmd(&app{cfg: &config.Config{}})
	if cmd.Use != "smoke [model-id]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	timeout, err := cmd.Flags().GetDuration("timeout")
	if err != nil || timeout != 180*time.Second {
		t.Fatalf("timeout default = %v, err = %v", timeout, err)
	}
	if v, _ := cmd.Flags().GetBool("json"); v {
		t.Fatal("json flag default should be false")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wt/... -run 'TestResolveSmokeModel|TestPickModelInteractive|TestFilterOnlyAgents|TestPrintSmokeHuman|TestPrintSmokeJSON|TestSmokeCmdFlags' -v`
Expected: compile failure — none of `resolveSmokeModel`, `pickModelInteractive`, `filterOnlyAgents`, `printSmokeHuman`, `printSmokeJSON`, `smokeJSONReport`, `smokeCmd` exist yet.

- [ ] **Step 3: Implement `cmd/wt/smoke.go`**

```go
// wt smoke — given a model, finds every agent currently eligible for it
// and runs a one-shot prompt through each via wt's real in-process launch
// machinery (agents.BuildLaunchCmd), reporting PASS/FAIL/SKIP. Distinct
// from `make test-agents` (agents-smoke.sh's hand-curated regression
// matrix): this command answers "is this model healthy across wt right
// now" for whichever model you point it at.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/spf13/cobra"
)

// smokeExit is a test seam wrapping os.Exit. Production sets the process
// exit code to 1 when any row FAILed (after the report has already been
// printed); tests override it to record the code instead of terminating
// the test process.
var smokeExit = os.Exit

func smokeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smoke [model-id]",
		Short: "Run a one-shot prompt through every agent eligible for a model",
		Long: "Find every agent currently eligible for a model and run a one-shot\n" +
			"prompt through each, using wt's real launch machinery. Reports\n" +
			"PASS/FAIL/SKIP per agent with enough detail to diagnose a failure\n" +
			"without re-running anything by hand.\n\n" +
			"With no model-id, lists every currently eligible model and prompts\n" +
			"for a choice (requires a TTY).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}

			var modelID string
			if len(args) > 0 {
				modelID = args[0]
			}
			m, err := resolveSmokeModel(a.cfg, modelID)
			if err != nil {
				return err
			}

			eligible := smoke.EligibleAgents(a.cfg, m)
			if len(eligible) == 0 {
				return fmt.Errorf("model %q has no currently eligible agents", m.ID)
			}
			agentsToRun, err := filterOnlyAgents(eligible, mustGetString(cmd, "only"))
			if err != nil {
				return err
			}

			promptOverride := mustGetString(cmd, "prompt")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			jsonOut, _ := cmd.Flags().GetBool("json")

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			runID := smoke.NewRunID()
			rows := make([]smoke.RowResult, 0, len(agentsToRun))
			for _, agentName := range agentsToRun {
				prompt, sentinel := promptOverride, ""
				if promptOverride == "" {
					prompt, sentinel = smoke.DefaultPrompt(agentName, runID)
				}
				rows = append(rows, smoke.RunRow(a.cfg, agentName, m, prompt, sentinel, timeout, cwd))
			}

			var anyFail bool
			if jsonOut {
				anyFail, err = printSmokeJSON(cmd.OutOrStdout(), runID, m.ID, rows)
			} else {
				anyFail, err = printSmokeHuman(cmd.OutOrStdout(), runID, m.ID, rows)
			}
			if err != nil {
				return err
			}
			if anyFail {
				smokeExit(1)
			}
			return nil
		},
	}
	cmd.Flags().String("prompt", "", "Override the default sentinel prompt (weakens verification to exit-code-only)")
	cmd.Flags().Duration("timeout", 180*time.Second, "Per-agent timeout")
	cmd.Flags().String("only", "", "Comma-separated agents to restrict the run to")
	cmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

// resolveSmokeModel resolves the model to test: the pinned id if given
// (validated against the currently eligible set, with a message
// distinguishing "unknown" from "exists but not eligible right now"), or
// an interactive pick from the eligible list when omitted.
func resolveSmokeModel(cfg *config.Config, modelID string) (config.Model, error) {
	all := smoke.AllEligibleModels(cfg)
	if modelID != "" {
		if idx := config.IndexModelByID(all, modelID); idx >= 0 {
			return all[idx], nil
		}
		if idx := config.IndexModelByID(cfg.Models, modelID); idx >= 0 {
			return config.Model{}, fmt.Errorf(
				"model %q is not currently eligible for any agent (not exposed, or a local "+
					"model that isn't running — check `modelman start %s` or `modelman litellm status`)",
				modelID, modelID)
		}
		return config.Model{}, fmt.Errorf("unknown model %q", modelID)
	}
	if len(all) == 0 {
		return config.Model{}, fmt.Errorf("no models are currently eligible for any agent — expose a cloud model or start a local one with `modelman start <id>`")
	}
	if !stdinTTY() {
		return config.Model{}, fmt.Errorf("wt smoke needs a TTY to list models; pass a model id directly (wt smoke <provider>/<name>)")
	}
	return pickModelInteractive(os.Stdin, os.Stdout, all)
}

// pickModelInteractive prints a numbered list of models to w and reads a
// selection from r. Takes an explicit reader/writer (rather than os.Stdin/
// os.Stdout directly) so tests can drive it without a real TTY.
func pickModelInteractive(r io.Reader, w io.Writer, models []config.Model) (config.Model, error) {
	fmt.Fprintln(w, "Eligible models:")
	for i, m := range models {
		fmt.Fprintf(w, "  %d) %s\n", i+1, m.ID)
	}
	fmt.Fprint(w, "Select a model: ")
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		return config.Model{}, err
	}
	line = strings.TrimSpace(line)
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(models) {
		return config.Model{}, fmt.Errorf("invalid selection %q", line)
	}
	return models[n-1], nil
}

// filterOnlyAgents narrows eligible to the comma-separated --only list,
// erroring before anything runs if a requested agent isn't eligible for
// this model (fail fast, mirrors agents-smoke.sh's validate_only_agents).
// An empty only returns eligible unchanged.
func filterOnlyAgents(eligible []string, only string) ([]string, error) {
	wanted := config.ParseFilterList(only)
	if len(wanted) == 0 {
		return eligible, nil
	}
	eligibleSet := make(map[string]bool, len(eligible))
	for _, a := range eligible {
		eligibleSet[a] = true
	}
	for _, w := range wanted {
		if !eligibleSet[w] {
			return nil, fmt.Errorf("agent %q is not eligible for this model (eligible: %s)", w, strings.Join(eligible, ", "))
		}
	}
	return wanted, nil
}

// printSmokeHuman renders the per-row lines (with an expanded detail block
// for every FAIL) and the trailing counts line. Returns whether any row
// FAILed.
func printSmokeHuman(w io.Writer, runID, modelID string, rows []smoke.RowResult) (bool, error) {
	var pass, fail, skip int
	for _, r := range rows {
		fmt.Fprintf(w, "[%-5s] %-10s %s (%s)\n", r.Status, r.Agent, modelID, formatSmokeDuration(r.Duration))
		switch r.Status {
		case smoke.StatusPass:
			pass++
		case smoke.StatusSkip:
			skip++
		default:
			fail++
			fmt.Fprintf(w, "  command: %s\n", r.Command)
			fmt.Fprintf(w, "  exit code: %d\n", r.ExitCode)
			if r.Err != nil {
				fmt.Fprintf(w, "  error: %v\n", r.Err)
			}
			if r.Output != "" {
				fmt.Fprintln(w, "  output:")
				for _, line := range strings.Split(strings.TrimRight(r.Output, "\n"), "\n") {
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}
	fmt.Fprintf(w, "=== PASS: %d FAIL: %d SKIP: %d (model=%s, runid=%s) ===\n", pass, fail, skip, modelID, runID)
	return fail > 0, nil
}

func formatSmokeDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// smokeJSONRow and smokeJSONReport are --json's wire schema — the contract
// a scripted caller (a future agents-smoke.sh refactor, or an LLM agent
// parsing output) consumes.
type smokeJSONRow struct {
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Command    string `json:"command"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
}

type smokeJSONReport struct {
	RunID string         `json:"run_id"`
	Model string         `json:"model"`
	Rows  []smokeJSONRow `json:"rows"`
}

// printSmokeJSON encodes rows as the --json contract and returns whether
// any row FAILed.
func printSmokeJSON(w io.Writer, runID, modelID string, rows []smoke.RowResult) (bool, error) {
	report := smokeJSONReport{RunID: runID, Model: modelID, Rows: make([]smokeJSONRow, 0, len(rows))}
	var anyFail bool
	for _, r := range rows {
		errStr := ""
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		if r.Status == smoke.StatusFail {
			anyFail = true
		}
		report.Rows = append(report.Rows, smokeJSONRow{
			Agent:      r.Agent,
			Model:      r.Model,
			Status:     string(r.Status),
			ExitCode:   r.ExitCode,
			DurationMS: r.Duration.Milliseconds(),
			Command:    r.Command,
			Output:     r.Output,
			Error:      errStr,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return false, err
	}
	return anyFail, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wt/... -run 'TestResolveSmokeModel|TestPickModelInteractive|TestFilterOnlyAgents|TestPrintSmokeHuman|TestPrintSmokeJSON|TestSmokeCmdFlags' -v`
Expected: PASS for all.

- [ ] **Step 5: Run vet and build**

Run: `go vet ./cmd/wt/... && go build ./...`
Expected: no vet issues; module builds. `smokeCmd` is not yet wired into `rootCmd()` (that's Task 4), but it compiles fine and is already exercised by this task's own `TestSmokeCmdFlags` — Go does not flag an unused top-level function the way it flags an unused import or local variable.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/smoke.go cmd/wt/smoke_test.go
git commit -m "wt: add cmd/wt/smoke.go CLI layer for wt smoke

resolveSmokeModel/pickModelInteractive/filterOnlyAgents handle model
resolution and --only filtering; printSmokeHuman/printSmokeJSON
render the PASS/FAIL/SKIP report. Not yet wired into rootCmd -
completes plan item #3"
```

---

## Task 4: Register the command, docs, final verification

**Files:**
- Modify: `cmd/wt/main.go:383` (`rootCmd()`'s `cmd.AddCommand(...)` call)
- Modify: `CLAUDE.md` (this repo's `wt/CLAUDE.md`) — capability table, package table, cmd/wt file table, package list, `## Smoke test` command block, new `## Smoke test (\`wt smoke\`)` section
- Create: `docs/wt-smoke.md`

**Interfaces:**
- Consumes: `smokeCmd(a *app) *cobra.Command` (Task 3).
- Produces: nothing further downstream — this is the final task.

- [ ] **Step 1: Register `smokeCmd` in `rootCmd()`**

In `cmd/wt/main.go`, change:

```go
	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a))
	return cmd
}
```

to:

```go
	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a))
	return cmd
}
```

- [ ] **Step 2: Verify registration with a quick integration check**

Run: `go run ./cmd/wt smoke --help`
Expected: prints `wt smoke`'s help text (the `Use`/`Short`/`Long` strings from Task 3), confirming the subcommand is reachable from the real CLI tree.

- [ ] **Step 3: Update `wt/CLAUDE.md`**

Insert a new row in the Optional Capabilities table — find:

```
| `Resumer` | `ResumeFlag()` + `LatestSession(path)` for resume support | claude, opencode |

Drivers without `Resumer` (codex, copilot, pi, agy, shell) never resume
```

replace with:

```
| `Resumer` | `ResumeFlag()` + `LatestSession(path)` for resume support | claude, opencode |
| `OneShotRunner` | `OneShotArgs(prompt)` — non-interactive single-prompt invocation, consumed by `wt smoke` | claude, codex, copilot, opencode, pi, agy |

Drivers without `Resumer` (codex, copilot, pi, agy, shell) never resume
```

Insert a new row in the package table — find:

```
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | `block-main-commit` pre-commit hook |
```

replace with:

```
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/smoke/` | `wt smoke`'s testable core: `EligibleAgents`, `AllEligibleModels`, `RunRow` (PASS/FAIL/SKIP classification via the `buildAndRun` seam) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
```

Insert a new row in the `cmd/wt/*.go` table — find:

```
| `cmd/wt/stats.go` | `wt stats` command — read-only report over `survey.jsonl` |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
```

replace with:

```
| `cmd/wt/stats.go` | `wt stats` command — read-only report over `survey.jsonl` |
| `cmd/wt/smoke.go` | `wt smoke` command — one-shot model×agent smoke test, human/JSON output |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
```

Add `smoke` to the package list — find:

```
Package list: `internal/{config,rotation,usage,refcount,survey,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,localgate}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts
```

replace with:

```
Package list: `internal/{config,rotation,usage,refcount,survey,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,localgate,smoke}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts
```

Add `wt smoke` to the "## Smoke test" bash block — find:

```
make test-agents        # live one-shot smoke: every agent × configured models, both routing modes
                       # (flips [litellm].enabled off/on in modelman.toml — resolved at ${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/modelman.toml, the path wt reads — and restores it afterwards; --modes current for one pass)
```

replace with:

```
wt smoke <model-id>     # one-shot smoke test: every agent currently eligible for one model
                       # (live routing only, read-only against modelman.toml; see docs/wt-smoke.md)
make test-agents        # live one-shot smoke: every agent × configured models, both routing modes
                       # (flips [litellm].enabled off/on in modelman.toml — resolved at ${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/modelman.toml, the path wt reads — and restores it afterwards; --modes current for one pass)
```

Add a new section — find:

```
### Adding a new agent driver

See the `adding-a-wt-agent` skill.

## Guard (Go)
```

replace with:

```
### Adding a new agent driver

See the `adding-a-wt-agent` skill.

## Smoke test (`wt smoke`)

`wt smoke <model-id>` finds every agent currently eligible for one model and
runs a one-shot prompt through each via `agents.BuildLaunchCmd` (the same
in-process launch construction a real launch uses), reporting PASS/FAIL/SKIP.
Distinct from `make test-agents`/agents-smoke.sh's hand-curated regression
matrix (static agent×model list, both routing modes) — `wt smoke` tests
whatever routing mode is live right now, against whichever model you point it
at. Read-only against modelman-owned state; never starts/stops local models
or flips LiteLLM routing. See `docs/wt-smoke.md`.

## Guard (Go)
```

- [ ] **Step 4: Create `docs/wt-smoke.md`**

```markdown
# `wt smoke`

Finds every agent currently eligible for one model and runs a one-shot
prompt through each, using wt's real launch machinery
(`agents.BuildLaunchCmd`) — the same construction a real `wt -A <agent> -M
<id>` launch uses. Reports PASS/FAIL/SKIP per agent with enough detail to
diagnose a failure without re-running anything by hand.

This is a live debugging tool over whichever model you point it at right
now, distinct from `make test-agents` (`agents-smoke.sh`'s hand-curated
regression matrix across a fixed model list and both routing modes).

```bash
wt smoke                        # list eligible models, prompt for a pick (needs a TTY)
wt smoke ollama/qwen3.8:27b-mlx # test one model directly, no TTY needed
wt smoke <model-id> --only claude,codex
wt smoke <model-id> --timeout 60s
wt smoke <model-id> --prompt "explain what 2+2 is"
wt smoke <model-id> --json
```

- `model-id` — a registry model id (`provider/name`). Omitted → interactive
  picker over every currently eligible model (cloud-exposed, or local and
  live-verified-running).
- `--prompt` — override the default sentinel-echo prompt. A custom prompt
  cannot be verified for a sentinel it was never asked to produce, so
  verification degrades to "the agent exited 0 within the timeout" — this
  confirms wiring, not response correctness.
- `--timeout` — per-agent timeout (default `180s`).
- `--only` — comma-separated agents to restrict the run to; must be a
  subset of the model's currently eligible agents.
- `--json` — emit a machine-readable report instead of the human table.

## Eligibility

An agent is eligible for a model when the model's provider is in the
agent's `supported_providers` **and** the model passes the same
exposure/local-running-gate checks a real launch would (`Config.EligibleModels`
+ `internal/localgate.Apply`) — the exact rules `wt`'s own launch path
applies, not a separate matrix.

## Output

Human (default): one line per agent, `[STATUS] agent model (duration)`.
Every FAIL row gets an indented detail block (command, exit code, captured
output) immediately after it; PASS/SKIP stay single-line. A trailing
`=== PASS: n FAIL: n SKIP: n (model=..., runid=...) ===` line summarizes the
run.

```
$ wt smoke ollama/qwen3.8:27b-mlx
[PASS ] claude     ollama/qwen3.8:27b-mlx (4.2s)
[FAIL ] codex      ollama/qwen3.8:27b-mlx (180.0s)
  command: codex exec ...
  exit code: 0
  error: timed out after 3m0s
[SKIP ] copilot    ollama/qwen3.8:27b-mlx (0.0s)
=== PASS: 1 FAIL: 1 SKIP: 1 (model=ollama/qwen3.8:27b-mlx, runid=run-a1b2c3d4) ===
```

`--json` emits `{"run_id", "model", "rows": [{"agent", "model", "status",
"exit_code", "duration_ms", "command", "output", "error"}]}`.

Exit code: `0` if every non-SKIP row PASSed, `1` if any row FAILed. SKIP
(agent binary not installed) never affects the exit code.

## Out of scope

`wt smoke` never starts, stops, or isolates local model providers (use
`modelman start`/`stop`) and never flips LiteLLM routing — it tests
whatever is live right now. See
`docs/superpowers/specs/2026-09-16-wt-smoke-design.md` for the full design
rationale.
```

- [ ] **Step 5: Full verification**

Run, from `wt/`:

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: clean build, no vet issues, all tests pass (existing suite + every test added in Tasks 1-3).

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/main.go CLAUDE.md docs/wt-smoke.md
git commit -m "wt: register wt smoke and document it

Wires smokeCmd into rootCmd(), updates the capability/package/file
tables and smoke-test command block in CLAUDE.md, and adds
docs/wt-smoke.md - completes plan item #4"
```
