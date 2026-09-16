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
