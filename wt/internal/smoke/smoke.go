// Package smoke implements wt smoke's testable core: given a model, find
// every currently eligible agent and run a one-shot prompt through each,
// classifying the result. Process execution goes through the buildAndRun
// seam so tests can verify classification logic (PASS/FAIL/SKIP, sentinel
// matching, timeouts) without executing real agent binaries.
package smoke

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

// smokeProbe is a test seam over the live inventory, so no test in this
// package probes a real provider.
var smokeProbe = localmodels.Inventory

// SetSmokeProbeForTest makes Eligibility read snap instead of the live
// inventory and returns the func restoring the real seam. internal/smoke's
// own tests assign smokeProbe directly; this hook exists for another
// package's tests — cmd/wt's smoke tests exercise this package's Eligibility
// through resolveSmokeModel and cannot assign an unexported var from there —
// mirroring internal/config's Set...ForTest setters, the codebase's
// convention for cross-package test state. Tests only.
func SetSmokeProbeForTest(snap localmodels.Snapshot) (restore func()) {
	old := smokeProbe
	smokeProbe = func(*config.Config) localmodels.Snapshot { return snap }
	return func() { smokeProbe = old }
}

// SetBuildAndRunForTest replaces the agent-run seam with fn, which reports a
// row's combined output and exit code (a non-zero code makes the row FAIL), and
// returns the func restoring the real seam. It exists for cmd/wt's tests, which
// cannot assign this package's unexported buildAndRun. Tests only.
func SetBuildAndRunForTest(fn func(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) ExecOutcome) (restore func()) {
	old := buildAndRun
	buildAndRun = func(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) execOutcome {
		return fn(cfg, agentName, m, prompt, cwd, timeout, profileApplier, cleanup)
	}
	return func() { buildAndRun = old }
}

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

// Eligibility takes ONE live inventory snapshot and walks the same catalog
// rows the picker and the non-TUI launch path read, returning both the
// deduped, id-sorted union of eligible models (AllEligibleModels' answer)
// and, per model id, the sorted list of eligible agents (EligibleAgents'
// answer). A model is eligible when selecting it would launch: a cloud row,
// or a local row the probe reports as running — plus discovered running
// rows, which a -M pin can launch — minus discovered rows routed through
// LiteLLM, which the picker makes unselectable and the CLI refuses. Because
// the rows are the same ones a real launch consults, wt smoke cannot
// advertise a model a real `wt -A <agent> -M <id>` launch would refuse. It
// never starts or stops anything itself; `Candidates` plus the caller's start
// step is how `wt smoke` handles idle local models. wt smoke's command layer calls this
// once per invocation and derives both answers from the result, instead of
// calling EligibleAgents and AllEligibleModels back to back, which would
// each pay their own inventory round.
func Eligibility(cfg *config.Config) (models []config.Model, agentsForModel map[string][]string) {
	agentsForModel = map[string][]string{}
	for _, c := range walk(cfg, false) {
		models = append(models, c.Row.Model)
		agentsForModel[c.Row.Model.ID] = c.Agents
	}
	return models, agentsForModel
}

// EligibleAgents returns, sorted, every non-command agent currently
// eligible to launch model m. A thin Eligibility wrapper for callers that
// only need one model's agent list; callers that also need
// AllEligibleModels in the same invocation should call Eligibility directly
// to share one inventory snapshot instead of calling both wrappers.
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

// ExecOutcome is the exported alias for execOutcome, used by cross-package
// test stubs (cmd/wt's smoke tests) that cannot access the unexported type.
type ExecOutcome = execOutcome

// buildAndRun builds and runs one agent's one-shot launch command. It is a
// package-level var so tests can stub it with canned outcomes, following
// the seam pattern documented in wt/CLAUDE.md's Go Tests section.
var buildAndRun = realBuildAndRun

// ProfileApplier applies a resolved profile to an already-built exec.Cmd.
// oneShotArgs is the one-shot invocation this row is about to run (e.g.
// codex's ["exec", prompt]) — the applier, not the caller, is responsible
// for appending it to cmd.Args, because where it belongs depends on the
// profile's mechanism: after any CLI flags the profile itself injects (a
// codex "-c"/"--profile" flag trailing "exec <prompt>" makes codex silently
// drop an earlier provider override), but before a wrapper mechanism runs
// (which must splice the complete command, prompt included, into its own
// argv). It returns a cleanup function that MUST be called after the
// command exits (success or failure) to restore any config file a
// profile's config_content mechanism rewrote, and an error if the profile
// itself could not be applied (e.g. wrapper binary missing). A nil
// ProfileApplier is a no-op.
type ProfileApplier func(cmd *exec.Cmd, oneShotArgs []string) (cleanup func() error, err error)

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

// StubOutcome constructs an execOutcome for tests that stub buildAndRun
// and need to return a canned result (e.g. exit code 0 with some output).
func StubOutcome(output string, exitCode int) execOutcome {
	return execOutcome{Output: output, ExitCode: exitCode}
}

func realBuildAndRun(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) execOutcome {
	// Always initialize cleanup to a no-op so RunRow's defer cleanup() is
	// safe even when we return early (BuildLaunchCmd failure, no OneShotRunner).
	*cleanup = func() error { return nil }
	cmd, err := agents.BuildLaunchCmd(agentName, m, cwd, false, nil, cfg, nil)
	if err != nil {
		return execOutcome{StartErr: err}
	}
	d := agents.ByName(agentName)
	osr, ok := d.(agents.OneShotRunner)
	if !ok {
		return execOutcome{StartErr: fmt.Errorf("agent %q does not support one-shot invocation", agentName)}
	}
	oneShotArgs := osr.OneShotArgs(prompt)

	buf := &boundedWriter{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	// Apply the profile before Start so env/args modifications take effect.
	// The cleanup is deferred by the caller (RunRow) so it runs regardless
	// of whether the agent exits successfully or times out. oneShotArgs is
	// threaded through the applier rather than appended here first: it must
	// land AFTER any profile-injected CLI flags for an agent like codex
	// (see ProfileApplier's doc comment — codex silently drops an earlier
	// provider override when "-c"/"--profile" flags trail "exec <prompt>"),
	// which the applier alone knows how to place correctly.
	if profileApplier != nil {
		// Snapshot before calling the applier: an applier may mutate
		// cmd.Env/cmd.Args before failing partway through (e.g. env/args
		// applied, then a wrapper binary turns out to be missing).
		// Restoring this snapshot on error guarantees "proceeding
		// unprofiled" below is never half-profiled, regardless of how the
		// specific ProfileApplier implementation handles its own failure.
		origEnv := slices.Clone(cmd.Env)
		origArgs := slices.Clone(cmd.Args)
		var applyErr error
		*cleanup, applyErr = profileApplier(cmd, oneShotArgs)
		if applyErr != nil {
			cmd.Env = origEnv
			// oneShotArgs must still land on the reverted, unprofiled
			// argv — the applier failed before (or while) placing it, but
			// the command still needs its one-shot subcommand/prompt to
			// run at all.
			cmd.Args = append(origArgs, oneShotArgs...)
			fmt.Fprintf(os.Stderr, "wt: profile apply: %v (proceeding unprofiled)\n", applyErr)
			*cleanup = func() error { return nil }
		}
	} else {
		cmd.Args = append(cmd.Args, oneShotArgs...)
	}
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
//
// profileApplier, when non-nil, is called after the launch command is built
// but before execution to apply a local-model profile overlay (env, args,
// config_file, wrapper). It returns a cleanup func that the caller MUST
// invoke after the command exits (success or failure) to restore any config
// file a profile's config_content mechanism rewrote. A nil profileApplier
// is a no-op.
func RunRow(cfg *config.Config, agentName string, m config.Model, prompt, sentinel string, timeout time.Duration, cwd string, profileApplier ProfileApplier) RowResult {
	start := time.Now()
	var cleanup func() error
	out := buildAndRun(cfg, agentName, m, prompt, cwd, timeout, profileApplier, &cleanup)
	defer func() {
		if cerr := cleanup(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
		}
	}()
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
