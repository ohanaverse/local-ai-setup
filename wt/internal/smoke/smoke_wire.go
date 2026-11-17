package smoke

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// CandidatesWire is Candidates over the model server's rows: the same rules
// (a launch row, or a start row a non-running local model has; a blocked row
// never; no command agent), applied to what the server decided. The agent's
// own route is not re-judged here: every client reaches the model through the
// gateway, which makes the per-agent protocol route irrelevant (R5), and the
// server already blocks an Unmapped row. It starts nothing.
func CandidatesWire(rows []modelapi.Row, ags []config.Agent) []Candidate {
	return walkWire(rows, ags, true)
}

// EligibilityWire is Eligibility over the model server's rows: the deduped,
// id-sorted models some non-command agent could launch as they are (no start
// row), and per model id the sorted agents that could.
func EligibilityWire(rows []modelapi.Row, ags []config.Agent) (models []config.Model, agentsForModel map[string][]string) {
	agentsForModel = map[string][]string{}
	for _, c := range walkWire(rows, ags, false) {
		models = append(models, c.Row.Model)
		agentsForModel[c.Row.Model.ID] = c.Agents
	}
	return models, agentsForModel
}

func walkWire(rows []modelapi.Row, ags []config.Agent, includeStart bool) []Candidate {
	byID := map[string]*Candidate{}
	for _, a := range ags {
		if agents.IsCommand(a.Name) {
			continue
		}
		for _, w := range catalog.EligibleWire(rows, a, "", "") {
			r := catalog.FromWire(w)
			act := r.Action()
			if act == catalog.ActionBlock || (act == catalog.ActionStart && !includeStart) {
				continue
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

// RowOptions is the per-call knobs of RunRowGateway. The zero value is the
// real behavior: production passes RowOptions{}.
type RowOptions struct {
	// Run, when set, replaces the agent run: it reports a row's output and
	// exit code (a non-zero code makes the row FAIL) for the row's working
	// directory cwd, and sets *cleanup. Tests pass it to classify canned
	// outcomes without running an agent.
	Run func(gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt, cwd string, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) ExecOutcome
}

// gatewayRun is the agent-run function o selects: the real one by default.
func (o RowOptions) gatewayRun() func(ctx context.Context, gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt string, rowDir RowDir, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) execOutcome {
	if o.Run == nil {
		return realGatewayRun
	}
	return func(_ context.Context, gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt string, rowDir RowDir, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) execOutcome {
		cwd, err := rowDir()
		if err != nil {
			*cleanup = func() error { return nil }
			return execOutcome{StartErr: err}
		}
		return o.Run(gw, models, agentName, m, prompt, cwd, timeout, profileApplier, cleanup)
	}
}

func realGatewayRun(ctx context.Context, gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt string, rowDir RowDir, timeout time.Duration, profileApplier ProfileApplier, cleanup *func() error) execOutcome {
	return runAgent(ctx, agentName, m, prompt, rowDir, timeout, profileApplier, cleanup, func(yolo bool) (*exec.Cmd, agents.LaunchInfo, error) {
		return agents.BuildLaunchCmdGateway(agentName, m, "", yolo, gw, models, nil)
	})
}

// GatewayDownReason is the SKIP reason of a row whose gateway kept answering
// 503 (R12): the model server owns the proxy, so a run that cannot reach it
// says nothing about the model.
const GatewayDownReason = "gateway unavailable (503) — the model server may be restarting LiteLLM"

// probeBound caps the one status probe after a failed row.
const probeBound = 5 * time.Second

// The probe goes through a modelclient.Client built from gw (RunRowGateway's
// signature is fixed and its ready is a bare func), so it has the client's
// dial timeout, redirect refusal and proxy policy.
//
// gatewayIs503 asks the gateway's model list once and reports whether it
// answers 503. Any other answer, and any failure to ask, is false: only a
// 503 says the proxy was restarting under the row.
func gatewayIs503(ctx context.Context, gw agents.Gateway) bool {
	pctx, cancel := context.WithTimeout(ctx, probeBound)
	defer cancel()
	status, err := modelclient.New(modelclient.Endpoint{URL: gw.Origin, Token: gw.Token}).GatewayStatus(pctx)
	return err == nil && status == http.StatusServiceUnavailable
}

// RunRowGateway (opts: RowOptions{} outside tests) runs one agent's one-shot row over the gateway: the one-shot run and the
// same classification, with the launch built by agents.BuildLaunchCmdGateway
// (the endpoint origin and token, never a provider's). ready is
// modelclient.Client.WaitGatewayReady: it is called before the row, and once
// more when the row FAILs while the gateway answers 503 (another client
// session's start or auto-stop restarts the proxy), after which the row is
// run once more and its result stands. A gateway that stays down (ready
// returns *modelclient.GatewayDownError) makes the row SKIP with
// GatewayDownReason, never FAIL (R12). wt retries only what it drives: an
// agent that fails on a 503 inside its own run owns its own retries. A native
// model never goes to the gateway, so it neither waits for it nor is probed.
func RunRowGateway(ctx context.Context, gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt, sentinel string, timeout time.Duration, rowDir RowDir, profileApplier ProfileApplier, ready func(context.Context) error, opts RowOptions) RowResult {
	start := time.Now()
	skip := func() RowResult {
		return RowResult{Agent: agentName, Model: m.ID, Status: StatusSkip, Duration: time.Since(start), Err: errors.New(GatewayDownReason)}
	}
	viaGateway := !m.Native
	if viaGateway {
		if err := ready(ctx); err != nil {
			var down *modelclient.GatewayDownError
			if errors.As(err, &down) {
				return skip()
			}
			return RowResult{Agent: agentName, Model: m.ID, Status: StatusFail, Duration: time.Since(start), Err: readyFailure(ctx, err)}
		}
	}
	res, out := runGatewayOnce(ctx, gw, models, agentName, m, prompt, sentinel, timeout, rowDir, profileApplier, opts)
	if res.Status != StatusFail || !viaGateway || ctx.Err() != nil || out.Interrupted || out.ModelFallback != "" || !gatewayIs503(ctx, gw) {
		return res
	}
	if err := ready(ctx); err != nil {
		var down *modelclient.GatewayDownError
		if errors.As(err, &down) {
			return skip()
		}
		return res
	}
	res, _ = runGatewayOnce(ctx, gw, models, agentName, m, prompt, sentinel, timeout, rowDir, profileApplier, opts)
	res.Duration = time.Since(start)
	return res
}

// readyFailure is the error of a row whose readiness wait failed for a reason
// other than a gateway that stays down (the server unreachable, a refused
// token, Ctrl+C).
func readyFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("interrupted")
	}
	return fmt.Errorf("the model server gateway is not ready: %w", err)
}

// runGatewayOnce runs the agent once and classifies it.
func runGatewayOnce(ctx context.Context, gw agents.Gateway, models []config.Model, agentName string, m config.Model, prompt, sentinel string, timeout time.Duration, rowDir RowDir, profileApplier ProfileApplier, opts RowOptions) (RowResult, execOutcome) {
	start := time.Now()
	var cleanup func() error
	out := opts.gatewayRun()(ctx, gw, models, agentName, m, prompt, rowDir, timeout, profileApplier, &cleanup)
	defer func() {
		if cerr := cleanup(); cerr != nil {
			fmt.Fprintf(os.Stderr, "wt: warning: profile cleanup failed: %v\n", cerr)
		}
	}()
	return classifyRow(agentName, m, prompt, sentinel, timeout, start, out), out
}
