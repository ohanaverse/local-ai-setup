package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"

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
// "wt: LiteLLM route for <id> updated" line for a row it wrote and the
// "api_base set" line of a repaired row.
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
