package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// This file holds the route calls of a slot-profile launch. The launch-time
// check in routes.go (EnsureModelRoute) is for one model, restarts the proxy
// itself and never fails a launch. A profile launch routes several models,
// restarts at most once, and must stop when a route cannot be written.

// ErrNotRoutable is the cause when modelRouteChange declines a model: a
// native model, a cloud model sync would not route, or a model whose
// location does not resolve.
var ErrNotRoutable = errors.New("wt has no LiteLLM route for this model")

// RouteEnsureError names the model whose route could not be ensured.
type RouteEnsureError struct {
	ModelID string
	Err     error
}

func (e *RouteEnsureError) Error() string {
	return fmt.Sprintf("LiteLLM route for %s not written: %v", e.ModelID, e.Err)
}

func (e *RouteEnsureError) Unwrap() error { return e.Err }

// EnsureModelRoutes writes the missing LiteLLM route of each model, one
// locked write per model, and never restarts the proxy on success. Models
// with the same id are written once. owedIn says a restart is already owed
// by an earlier write of the same launch. It returns owed: owedIn, or true
// when a write here changed config.yaml.
//
// The writes are one per model because cloud and local models need different
// changes (modelRouteChange): litellm.Change.AddMissingOnly is a flag on the
// whole change.
//
// It returns an error, as a *RouteEnsureError, for: a model
// modelRouteChange declines (ErrNotRoutable); any error of the write
// itself, which includes litellm.ErrMissing, litellm.ErrRegistryRedirected
// and a config.yaml lock that was not free within ensureRouteLockTimeout;
// and an Outcome.Err of the model's own row. On an error it settles what
// is owed itself: one asynchronous restart (bounceProxyAsync) when owedIn
// or an earlier write of this call changed the file, and it returns
// owed == false. A caller about to exit still owes WaitPendingRoutes.
//
// What it prints goes to out (nil: stderr) through routePrintf: the
// "wt: LiteLLM route for <id> updated" line for a row it wrote, the
// "api_base set" line of a repaired row, and "wt: LiteLLM config.yaml
// updated" for a write that changed the file without writing the model's
// row or printing either of those (reportEnsured).
func EnsureModelRoutes(ctx context.Context, out io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (owed bool, err error) {
	ctx = withRouteOutput(ctx, out)
	owed = owedIn
	fail := func(id string, cause error) (bool, error) {
		if owed {
			// Routes written before this failure are in config.yaml. Publish
			// them, or the next launch of one of those models finds its row
			// in place, restarts nothing, and the proxy answers "Invalid
			// model name".
			bounceProxyAsync(ctx, cfg)
		}
		return false, &RouteEnsureError{ModelID: id, Err: cause}
	}
	seen := map[string]bool{}
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ch, ok := modelRouteChange(cfg, m)
		if !ok {
			return fail(m.ID, ErrNotRoutable)
		}
		lockCtx, cancel := context.WithTimeout(ctx, ensureRouteLockTimeout)
		res, werr := applyRoutes(cfg, ch, litellm.Options{NoRestart: true, Ctx: lockCtx})
		cancel()
		if werr != nil {
			return fail(m.ID, werr)
		}
		if res.Changed {
			owed = true
		}
		w := routeWrite{changed: res.Changed}
		var rowErr error
		for _, o := range res.Outcomes {
			switch {
			case o.Err != nil:
				// The change adds one row, so a rejected id is this model's.
				rowErr = o.Err
			case o.Action == litellm.ActionAPIBaseSet:
				w.said = true
				routePrintf(ctx, "wt: LiteLLM route for %s: api_base set\n", o.ID)
			case o.Written:
				w.rows = append(w.rows, o.ID)
			}
		}
		if rowErr != nil {
			return fail(m.ID, rowErr)
		}
		reportEnsured(ctx, w, ch.Add[0].ID)
	}
	return owed, nil
}

// proxyProbeTimeout bounds ProbeProxy.
const proxyProbeTimeout = 5 * time.Second

// RestartProxy restarts the LiteLLM proxy and waits for it to answer,
// returning what bounceProxyAsync only prints: the restart command's
// failure, or the readiness timeout. It first waits for any restart
// already in flight (WaitPendingRoutes), counts itself in routeWG and
// routesInFlight while it runs, and always waits for readiness
// (waitProxy, proxyReadyTimeout). ctx gives it its values only; its
// cancellation is dropped (context.WithoutCancel), because a restart
// cannot be called off.
//
// What it returns is also remembered as the outcome of the last restart
// attempt (lastRestart), nil included: a restart that worked clears an
// earlier failure, and one that failed fails the next ProbeProxy.
//
// An empty [litellm] url is an error wrapping config.ErrLitellmUnconfigured,
// returned before anything is restarted: there is no address to wait for.
// That is no restart attempt, so it is not remembered.
func RestartProxy(ctx context.Context, cfg *config.Config) (err error) {
	url := cfg.LitellmBaseURL()
	if url == "" {
		return fmt.Errorf("the LiteLLM proxy was not restarted: %w", config.ErrLitellmUnconfigured)
	}
	WaitPendingRoutes()
	routeWG.Add(1)
	routesInFlight.Add(1)
	defer routeWG.Done()
	defer routesInFlight.Add(-1)
	defer func() { setLastRestartErr(err) }()
	bg := context.WithoutCancel(ctx)
	if warnings := restartProxy(bg); len(warnings) > 0 {
		return errors.New(strings.Join(warnings, "; "))
	}
	return waitProxy(bg, url, proxyReadyTimeout)
}

// ProbeProxy is the last check before a slot-profile launch hands the proxy
// to an agent. It fails in three ways:
//
//   - An empty [litellm] url: an error wrapping config.ErrLitellmUnconfigured.
//   - The most recent proxy restart this process attempted failed (its
//     command failed, or the proxy did not answer afterwards), whether it was
//     RestartProxy or an asynchronous restart, such as the one a Start given
//     Options.RestartOwed settles the owed restart with. Error:
//     fmt.Errorf("the last LiteLLM proxy restart failed, so routes written
//     since may not be live: %w", err). It is returned before anything is
//     probed, because the probe cannot see this: the proxy that was not
//     restarted is usually still up and answers 200, while it answers
//     "Invalid model name" for every route written since it booted. The
//     failure is not cleared by being reported; it stands until a later
//     restart attempt succeeds.
//   - The proxy does not answer 200 on its liveness endpoint within
//     proxyProbeTimeout (one waitProxy call). Error:
//     fmt.Errorf("the LiteLLM proxy does not answer at %s: %w", url, err).
//
// It does not wait for a restart still in flight, whose outcome is not
// recorded until it finishes: the caller calls WaitPendingRoutes first.
func ProbeProxy(ctx context.Context, cfg *config.Config) error {
	url := cfg.LitellmBaseURL()
	if url == "" {
		return fmt.Errorf("the LiteLLM proxy cannot be probed: %w", config.ErrLitellmUnconfigured)
	}
	if err := lastRestartErr(); err != nil {
		return fmt.Errorf("the last LiteLLM proxy restart failed, so routes written since may not be live: %w", err)
	}
	if err := waitProxy(ctx, url, proxyProbeTimeout); err != nil {
		return fmt.Errorf("the LiteLLM proxy does not answer at %s: %w", url, err)
	}
	return nil
}
