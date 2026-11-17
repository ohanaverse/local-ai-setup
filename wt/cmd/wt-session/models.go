package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// modelSession is the client session's handle on the model server: the client
// and the endpoint it was built for.
type modelSession struct {
	client *modelclient.Client
	ep     modelclient.Endpoint
}

// newModelClient builds the client for ep. It is a test seam: cmd/wt-session's
// TestMain swaps in a client whose transport dials loopback only, so the
// fake model server is the one network a test may reach.
var newModelClient = func(ep modelclient.Endpoint) *modelclient.Client {
	return modelclient.New(ep)
}

// agentDriverNames is the agent drivers a session synthesizes an agent for:
// every registered driver but the commands (they have no model layer).
func agentDriverNames() []string {
	var out []string
	for _, n := range agents.Names() {
		if !agents.IsCommand(n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// sessionCfg is the config the client session reads: config.toml only, no
// registry (config.LoadSession). It is read once per app. The cache is
// unsynchronized: the main goroutine only. Host commands read
// hostConfig instead (R3).
func (a *app) sessionCfg() (*config.Config, error) {
	if !a.sessCfgSet {
		a.sessCfg, a.sessCfgErr = config.LoadSession(agentDriverNames()...)
		a.sessCfgSet = true
	}
	return a.sessCfg, a.sessCfgErr
}

// openModels resolves the endpoint from [model-server] and the environment,
// builds the client and pings. Every path that needs a model calls it (or
// buildModels) before it starts or launches anything: with nothing
// configured it is the *NotConfiguredError refusal (exit 1 through
// remoteExit), since the flip there is no other path. Each failure carries still, the clause saying
// what did not happen; the typed cause stays reachable with errors.As. A
// success is cached on the app. Callers must never wrap an openModels error
// with Still again: it already ends with the clause. The cache is
// unsynchronized, main goroutine only: a heartbeat goroutine uses ms.client,
// never openModels.
func (a *app) openModels(ctx context.Context, still string) (*modelSession, error) {
	if a.models != nil {
		return a.models, nil
	}
	ms, err := a.buildModels(still)
	if err != nil {
		return nil, err
	}
	if _, err := ms.client.Ping(ctx); err != nil {
		return nil, modelclient.Still(err, still)
	}
	a.models = ms
	return a.models, nil
}

// buildModels resolves the endpoint and builds the client without asking the
// server anything. The picker uses it directly: its first request pings, so an
// unreachable server shows as a failed fetch the user can retry with F instead
// of ending wt before the screen opens.
func (a *app) buildModels(still string) (*modelSession, error) {
	cfg, err := a.sessionCfg()
	if err != nil {
		return nil, modelclient.Still(err, still)
	}
	ms := cfg.ModelServer()
	if p := ms.Problems(); len(p) > 0 {
		return nil, modelclient.Still(errors.New(strings.Join(p, "; ")), still)
	}
	tokenFile := ms.TokenFile
	if tokenFile != "" {
		if tokenFile, err = config.ExpandHome(tokenFile); err != nil {
			return nil, modelclient.Still(err, still)
		}
	}
	ep, err := modelclient.ResolveEndpoint(ms.URL, tokenFile, os.Getenv)
	if err != nil {
		return nil, modelclient.Still(err, still)
	}
	return &modelSession{client: newModelClient(ep), ep: ep}, nil
}

// remoteExit is the one mapping from a model-server error to an exit status:
// 3 when the user declined a question, 2 when something started did not
// finish (a failed, detached or lost job, a lost server), 1 for everything
// else (could not start). The text is unchanged and printed once by main; an
// error that already carries a code keeps it.
func remoteExit(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := exitcode.Code(err); ok {
		return err
	}
	var (
		declined  *modelclient.DeclinedError
		job       *modelclient.JobError
		lost      *modelclient.LostServerError
		jobLost   *modelclient.JobLostError
		detached  *modelclient.DetachedError
		interrupt *interruptedStartError
	)
	switch {
	case errors.As(err, &declined):
		return exitcode.New(3, err, false)
	case errors.As(err, &job), errors.As(err, &lost), errors.As(err, &jobLost), errors.As(err, &detached), errors.As(err, &interrupt):
		return exitcode.New(2, err, false)
	}
	return exitcode.New(1, err, false)
}

// nativeFallback reports whether a launch whose model setup failed with err
// runs the agent natively instead (#615): only when no model server is
// configured at all (neither url nor token; one missing is a half-configured
// server and refuses) and the launch asks for no model (-M, -T, -F pick from a
// catalog that does not exist). A configured server is never bypassed.
func nativeFallback(err error, pinned, tags, family string) bool {
	return pinned == "" && tags == "" && family == "" && modelclient.NoServerConfigured(err)
}

// wouldLaunchNative reports whether a launch of agent with these filters would
// run natively (#615): a model-driven agent, or a picker that may need one
// (agent ""), with no model server configured at all and no model asked for.
func wouldLaunchNative(a *app, agent, pinned, tags, family string) bool {
	if agents.IsCommand(agent) {
		return false
	}
	_, err := a.buildModels(launchStill)
	return err != nil && nativeFallback(err, pinned, tags, family)
}
