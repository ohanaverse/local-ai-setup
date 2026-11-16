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

// newModelClient builds the client for ep. It is a test seam: cmd/wt's
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
// unsynchronized: the main goroutine only. a.cfg is not
// replaced: host commands keep config.Load() until the flip.
func (a *app) sessionCfg() (*config.Config, error) {
	if !a.sessCfgSet {
		a.sessCfg, a.sessCfgErr = config.LoadSession(agentDriverNames()...)
		a.sessCfgSet = true
	}
	return a.sessCfg, a.sessCfgErr
}

// endpointInEnv reports whether WT_MODEL_URL, WT_MODEL_TOKEN_FILE or
// WT_MODEL_TOKEN is set.
func endpointInEnv() bool {
	for _, k := range []string{"WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// endpointConfigured reports whether anything names a model server: a
// [model-server] url or token_file, or WT_MODEL_URL, WT_MODEL_TOKEN_FILE or
// WT_MODEL_TOKEN.
func (a *app) endpointConfigured() bool {
	if endpointInEnv() {
		return true
	}
	cfg, err := a.sessionCfg()
	if err != nil {
		// Safest reading: a config.toml that cannot be read is not "no
		// endpoint". Say configured, so openModels surfaces the load error
		// instead of a quiet fall back to the legacy path.
		return true
	}
	ms := cfg.ModelServer()
	// An inline token alone counts too: openModels reports it as a problem.
	return ms.URL != "" || ms.TokenFile != "" || ms.Token != ""
}

// openModels resolves the endpoint from [model-server] and the environment,
// builds the client and pings. Each failure carries still, the clause saying
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
