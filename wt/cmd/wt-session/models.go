package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// modelSession is the client session's handle on the model server: the client
// and the endpoint it was built for, and the hooks of the app that built it
// (the code that holds only a session reads them from here). hooks points at
// the app's own field (an app is always used by pointer), so a hook set on the
// app after the session is open is seen too.
type modelSession struct {
	client *modelclient.Client
	ep     modelclient.Endpoint
	hooks  *hooks
	// mode is the effective mode the session resolved: local or remote
	// (native and unset never build a session).
	mode modelclient.Mode
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

// openModels resolves the mode and the endpoint from [model-server] and the
// environment, builds the client and pings. Every path that needs a model
// calls it (or buildModels) before it starts or launches anything: an unset
// mode is the *ModeUnsetError refusal, native mode the *NativeModeError one,
// a local or remote mode missing a setting the *NotConfiguredError one (each
// exit 1 through remoteExit). A failed ping carries the mode's way out
// (pingHint). Each failure carries still, the clause saying
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
		return nil, modelclient.Still(pingHint(err, ms), still)
	}
	a.models = ms
	return a.models, nil
}

// buildModels resolves the endpoint and builds the client without asking the
// server anything. The picker uses it directly: its first request pings, so an
// unreachable server shows as a failed fetch the user can retry with F instead
// of ending wt before the screen opens.
//
// The outcome, a failure included, is read once per app and cached (main
// goroutine only): one launch asks several times whether a server is
// configured (the native decision, the slot-profile notes, the launch itself)
// and each ask must not re-read the config and the token file.
func (a *app) buildModels(still string) (*modelSession, error) {
	if !a.builtSet {
		a.built, a.builtErr = a.resolveModels()
		a.builtSet = true
	}
	if a.builtErr != nil {
		return nil, modelclient.Still(a.builtErr, still)
	}
	return a.built, nil
}

// resetModelCaches forgets every cached read of the model server's
// configuration (config.toml, the resolution and an opened session), so the
// next buildModels or openModels resolves afresh: the first-run gate calls it
// after wt config may have changed the mode.
func (a *app) resetModelCaches() {
	a.sessCfg, a.sessCfgErr, a.sessCfgSet = nil, nil, false
	a.built, a.builtErr, a.builtSet = nil, nil, false
	a.models = nil
}

// errNoModeChosen ends a launch whose first-run gate opened wt config and
// found the mode still unset.
var errNoModeChosen = errors.New("no model-server mode chosen — " + launchStill)

// ensureMode is the first-run gate of a launch that needs a model: it does
// nothing unless the effective mode is unset (a stored or environment mode of
// any kind, even an invalid one, is left to the launch's own handling). On a
// terminal it opens wt config on the Model server tab once, as a separate
// program run to completion, forgets every cached read and resolves again; a
// mode still unset ends the launch (exit 1). Without a terminal it returns
// the same refusal a script always got (ModeUnsetError with launchStill).
// It never opens the editor twice in one process.
func (a *app) ensureMode(_ *cobra.Command) error {
	_, err := a.buildModels(launchStill)
	if !modelclient.IsModeUnset(err) {
		return nil
	}
	if !stdinTTY() {
		return err
	}
	if eerr := runConfigEditor(os.Stdout, os.Stderr, a, configeditor.TabServer); eerr != nil {
		return modelclient.Still(eerr, launchStill)
	}
	a.resetModelCaches()
	if _, err := a.buildModels(launchStill); modelclient.IsModeUnset(err) {
		return errNoModeChosen
	}
	return nil
}

// resolveModels is buildModels' one uncached read: the effective mode
// (modelclient.ResolveMode over [model-server] and the environment), then,
// for local or remote, the endpoint and the client built from it. An invalid
// mode, an unset mode and native mode are refused before any endpoint work
// (no token file is read for them; native ignores every stray url and token
// value, an inline token included, which only earns a note; any other mode
// refuses an inline token); a stored local or remote with a field
// only the environment supplies resolves, one missing a setting everywhere
// is ResolveEndpoint's *NotConfiguredError.
func (a *app) resolveModels() (*modelSession, error) {
	cfg, err := a.sessionCfg()
	if err != nil {
		return nil, err
	}
	ms := cfg.ModelServer()
	res := modelclient.ResolveMode(ms.Mode, ms.URL, ms.TokenFile, os.Getenv)
	if res.Problem != "" {
		return nil, &modelclient.ModeProblemError{Problem: res.Problem}
	}
	// An explicit native ignores stray url and token values, an inline
	// token included (spec: "an explicit native ignores stray url/token
	// values"); a plaintext token still earns one note, without its value.
	if res.Mode == modelclient.ModeNative {
		if ms.Token != "" {
			fmt.Fprintln(osStderr, "wt: note: config.toml: [model-server] token is ignored in native mode — remove it (a token belongs in a token_file with mode 0600)")
		}
		return nil, &modelclient.NativeModeError{Source: res.Source}
	}
	// Every other mode refuses on the table's other problems (an inline
	// token); the stored mode's own problem is ResolveMode's to judge, since
	// WT_MODEL_MODE may override it.
	rest := ms
	rest.Mode = ""
	if p := rest.Problems(); len(p) > 0 {
		return nil, errors.New(strings.Join(p, "; "))
	}
	if res.Mode == "" {
		return nil, &modelclient.ModeUnsetError{}
	}
	tokenFile := ms.TokenFile
	if tokenFile != "" {
		if tokenFile, err = config.ExpandHome(tokenFile); err != nil {
			return nil, err
		}
	}
	ep, err := modelclient.ResolveEndpoint(ms.URL, tokenFile, os.Getenv)
	if err != nil {
		return nil, err
	}
	return &modelSession{client: a.hooks.client(ep), ep: ep, hooks: &a.hooks, mode: res.Mode}, nil
}

// pingHintError is a failed ping with the session mode's way out appended;
// the classified modelclient error stays reachable with errors.As.
type pingHintError struct {
	err  error
	hint string
}

func (e *pingHintError) Error() string { return e.err.Error() + "; " + e.hint }
func (e *pingHintError) Unwrap() error { return e.err }

// pingHint words a failed ping for the session's mode. local: the server is
// this machine's LaunchAgent, so the fix is the Model server tab or a
// kickstart of local.wt-model.serve for this user. remote: the url tried
// (without credentials, query or fragment) beside the classified error,
// which already says what went wrong there.
func pingHint(err error, ms *modelSession) error {
	switch ms.mode {
	case modelclient.ModeLocal:
		return &pingHintError{err: err, hint: fmt.Sprintf("local mode: start it from wt config (Model server tab) or run launchctl kickstart -k gui/%d/local.wt-model.serve", os.Getuid())}
	case modelclient.ModeRemote:
		return &pingHintError{err: err, hint: "remote mode, url " + displayURL(ms.ep.URL) + " — check the server there, or change the url or mode in wt config (Model server tab)"}
	}
	return err
}

// displayURL is raw safe to print: no user info, query or fragment, which
// may carry a secret.
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(the configured url does not parse)"
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
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
// runs the agent natively instead (#615): only when the effective mode is
// native (stored mode = "native" or WT_MODEL_MODE=native; an unset mode and
// a half-configured server refuse) and the launch asks for no model (-M, -T,
// -F pick from a catalog native mode does not have). A configured server is
// never bypassed. pinnedSet is whether -M was given at all: an explicit empty
// -M still asks for a model, so it refuses like any other.
func nativeFallback(err error, pinned string, pinnedSet bool, tags, family string) bool {
	return pinned == "" && !pinnedSet && tags == "" && family == "" && modelclient.IsNativeMode(err)
}

// wouldLaunchNative reports whether a launch of agent with these filters would
// run natively (#615): a model-driven agent, or a picker that may need one
// (agent ""), in native mode with no model asked for.
func wouldLaunchNative(a *app, agent, pinned string, pinnedSet bool, tags, family string) bool {
	if agents.IsCommand(agent) {
		return false
	}
	_, err := a.buildModels(launchStill)
	return err != nil && nativeFallback(err, pinned, pinnedSet, tags, family)
}
