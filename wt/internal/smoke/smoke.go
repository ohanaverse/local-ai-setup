// Package smoke implements wt smoke's testable core: given a model, find
// every currently eligible agent and run a one-shot prompt through each,
// classifying the result. Process execution goes through the buildAndRun
// seam so tests can verify classification logic (PASS/FAIL/SKIP, sentinel
// matching, timeouts) without executing real agent binaries.
package smoke

import (
	"crypto/rand"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"syscall"
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

// Eligibility runs a single localgate probe round and walks the agent×model
// eligibility rules once, returning both the union of eligible models
// (AllEligibleModels' answer) and, per model id, the sorted list of eligible
// agents (EligibleAgents' answer) — the same membership (provider support)
// and exposure/local-running-gate rules wt's own launch path applies via
// Config.EligibleModels and localgate.Apply, so a model wt smoke reports
// eligible is a model a real `wt -A <agent> -M <id>` launch would accept.
// wt smoke's command layer calls this once per invocation and derives both
// answers from the result, instead of calling EligibleAgents and
// AllEligibleModels back to back, which would each pay their own probe
// round (localgate.ResolveAll does a live HTTP probe, up to 2s per flagged
// non-ollama local model).
func Eligibility(cfg *config.Config) (models []config.Model, agentsForModel map[string][]string) {
	verified := localgate.ResolveAll(cfg)
	agentsForModel = map[string][]string{}
	seen := map[string]bool{}
	for _, a := range cfg.Agents {
		if agents.IsCommand(a.Name) {
			continue
		}
		eligible, err := cfg.EligibleModels(a.Name, "", "")
		if err != nil {
			continue
		}
		running := cfg.FilterToRunningLocal(eligible, verified)
		for _, m := range running {
			if !seen[m.ID] {
				seen[m.ID] = true
				models = append(models, m)
			}
			agentsForModel[m.ID] = append(agentsForModel[m.ID], a.Name)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	for id := range agentsForModel {
		sort.Strings(agentsForModel[id])
	}
	return models, agentsForModel
}

// EligibleAgents returns, sorted, every non-command agent currently
// eligible to launch model m. A thin Eligibility wrapper for callers that
// only need one model's agent list; callers that also need
// AllEligibleModels in the same invocation should call Eligibility directly
// to share one probe round instead of calling both wrappers.
func EligibleAgents(cfg *config.Config, m config.Model) []string {
	_, agentsForModel := Eligibility(cfg)
	return agentsForModel[m.ID]
}

// AllEligibleModels returns, sorted by id, the union of every model
// currently eligible for at least one non-command agent — the candidate
// list wt smoke's interactive picker offers when no model id is given.
func AllEligibleModels(cfg *config.Config) []config.Model {
	models, _ := Eligibility(cfg)
	return models
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

// maxCapturedOutput bounds how much of a row's combined stdout+stderr is
// kept, matching the design spec's "truncated to a bounded tail" for the
// FAIL detail block (human renderer) and the --json "output" field — both
// consumers inherit the same bound because truncation happens once, here,
// rather than being re-applied per renderer.
const maxCapturedOutput = 8192 // 8 KiB

const truncatedMarker = "...[truncated, showing last 8KiB]...\n"

// truncateOutput bounds s to its last maxCapturedOutput bytes, prefixing a
// marker line when truncation actually occurs so a reader knows output was
// cut. Left unchanged (no marker) when s already fits.
func truncateOutput(s string) string {
	if len(s) <= maxCapturedOutput {
		return s
	}
	return truncatedMarker + s[len(s)-maxCapturedOutput:]
}

// boundedWriter caps the process output held in memory to at most
// maxCapturedOutput bytes throughout the run, not just once the process
// exits or times out — a runaway agent (verbose logging, a stuck retry
// loop) that writes megabytes before the timeout never grows the captured
// buffer past the bound that's eventually kept anyway. String applies the
// same tail-keeping + marker convention as truncateOutput.
type boundedWriter struct {
	tail      []byte
	truncated bool
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.tail = append(b.tail, p...)
	if len(b.tail) > maxCapturedOutput {
		b.truncated = true
		b.tail = append([]byte(nil), b.tail[len(b.tail)-maxCapturedOutput:]...)
	}
	return len(p), nil
}

func (b *boundedWriter) String() string {
	if b.truncated {
		return truncatedMarker + string(b.tail)
	}
	return string(b.tail)
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

	buf := &boundedWriter{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmdLine := strings.Join(cmd.Args, " ")

	// cmd.Stdout/Stderr being the exact same io.Writer value makes os/exec
	// share one pipe and copy goroutine between them instead of racing two
	// writers on buf; cmd.Wait() blocks until
	// those goroutines see EOF. claude/codex/copilot/opencode are all
	// Node-based CLIs that can spawn descendants (MCP servers, language
	// server helpers, ...) — killing only the direct child on timeout can
	// leave a descendant holding the pipe's write end open, and Wait()
	// would then never return. WaitDelay bounds how long Wait() will keep
	// waiting on the copy goroutines past process exit/kill before giving
	// up anyway, and Setpgid puts the whole tree in one process group so
	// the timeout path below can kill child+descendants together. Both
	// must be set before Start().
	cmd.WaitDelay = 5 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		// Kill the whole process group (negative pid), not just the direct
		// child, so a surviving descendant can't keep Wait() blocked
		// forever. Errors are tolerated the same way the direct-child Kill
		// this replaced already did (the process may already be gone).
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
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
