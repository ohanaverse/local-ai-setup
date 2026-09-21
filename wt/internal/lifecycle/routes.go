package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Seams: production talks to the real config.yaml, proxy and stderr.
var (
	applyRoutes           = litellm.Apply
	waitProxy             = litellm.WaitReady
	probeProxy            = litellm.Listening
	routesWarn  io.Writer = os.Stderr
)

const (
	// proxyReadyTimeout bounds the wait for the proxy after a route change.
	proxyReadyTimeout = 30 * time.Second
	// proxyAliveTimeout bounds the single probe taken before the change.
	// Short on purpose: it is paid on every start and stop. It only has to
	// tell "nothing there" (refused: no wait) from "something there" (any
	// other outcome, even a timeout: wait for it after the restart).
	proxyAliveTimeout = 1500 * time.Millisecond
)

// restartMode says who bounces the proxy after a route write.
type restartMode int

const (
	restartIfChanged restartMode = iota // bounce (and wait) only when the write changed the file
	restartDeferred                     // write only; the caller owes the bounce
	restartForced                       // bounce (and wait) even if this write changed nothing
)

// routeAfterStart adds the started model's LiteLLM route. A single-model
// provider (omlx, mtplx) can serve only one model, so starting one replaced
// whatever ran before: its siblings' routes are removed in the same write.
// restartOwed means an earlier deferred write (the replaced occupant's route
// removal) has not been followed by a restart yet; this call's bounce settles
// it, so a replace costs one proxy restart, not two.
// It never fails the caller — a missing route is a warning, not a failed start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool) {
	mode := restartIfChanged
	if restartOwed {
		mode = restartForced
	}
	m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName)
	if !ok {
		fmt.Fprintf(routesWarn, "wt: %s/%s is not in the registry; no LiteLLM route added (register it with modelman)\n", t.ProviderID, t.ModelName)
		return
	}
	var remove []string
	if SingleModel(t.ProviderID) {
		for _, id := range familyModelIDs(cfg, t.ProviderID) {
			if id != m.ID {
				remove = append(remove, id)
			}
		}
	}
	applyAndReport(ctx, cfg, []string{m.ID}, remove, mode)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every one of its models
// loses its route. modelName may be "" for a provider-wide stop.
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID, modelName string) {
	routeRemove(ctx, cfg, providerID, modelName, restartIfChanged)
}

func routeRemove(ctx context.Context, cfg *config.Config, providerID, modelName string, mode restartMode) bool {
	var remove []string
	switch {
	case SingleModel(providerID):
		remove = familyModelIDs(cfg, providerID)
	default:
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			return false
		}
		remove = []string{m.ID}
	}
	return applyAndReport(ctx, cfg, nil, remove, mode)
}

// routeAfterOccupantStopped removes the route of the occupant a replace just
// stopped. It is the env hook defaultEnv wires in (env.go): the start that
// follows may still fail, and a failed Start runs no success hook at all, so
// the removal has to be written at the moment the occupant went down. It only
// writes: the proxy restart is deferred to Start's single settling bounce
// (routeAfterStart on success, bounceRoutes on failure). It reports whether a
// restart is now owed.
func routeAfterOccupantStopped(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool {
	return routeRemove(ctx, cfg, occ.ProviderID, occ.ModelName, restartDeferred)
}

// bounceRoutes restarts the proxy (and waits for it) to settle deferred route
// writes when the start that followed them failed.
func bounceRoutes(ctx context.Context, cfg *config.Config) {
	applyAndReport(ctx, cfg, nil, nil, restartForced)
}

// familyModelIDs lists the LiteLLM-managed local model ids whose provider
// shares providerID's family (omlx and omlx-6bit are one physical server).
func familyModelIDs(cfg *config.Config, providerID string) []string {
	fam := localmodels.Family(providerID)
	var ids []string
	for _, m := range litellm.LocalModels(cfg) {
		if localmodels.Family(m.ProviderID) == fam {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// applyAndReport reports whether config.yaml was written.
func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string, mode restartMode) bool {
	// Probe the proxy ONCE before the write (Listening: only a refused
	// connection counts as down, so a slow proxy is still waited for). Waiting for readiness afterwards
	// is only meaningful when a proxy was actually serving: a configured URL
	// with nothing listening (routing may even be switched off) used to cost
	// every start and stop the full proxyReadyTimeout waiting for a process
	// that was never up. The restart itself stays best-effort either way.
	url := cfg.LitellmBaseURL()
	wasUp := url != "" && probeProxy(ctx, url, proxyAliveTimeout)

	res, err := applyRoutes(cfg, add, remove, litellm.Options{
		SkipReadyGate: true,
		NoRestart:     mode == restartDeferred,
		ForceRestart:  mode == restartForced,
		// The caller's ctx, so Ctrl+C during a start/stop does not keep
		// paying for the restart command's full run time.
		Restart: func() []string { return litellm.RestartContext(ctx) },
	})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return false
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	if ctx.Err() == nil { // cancelled by the user: a failed restart is expected, stay quiet
		for _, w := range res.Warnings {
			fmt.Fprintf(routesWarn, "wt: %s\n", w)
		}
	}
	restarted := (res.Changed && mode != restartDeferred) || mode == restartForced
	if restarted && wasUp {
		if err := waitProxy(ctx, url, proxyReadyTimeout); err != nil && ctx.Err() == nil { // cancelled by the user: stay quiet
			fmt.Fprintf(routesWarn, "wt: %v\n", err)
		}
	}
	return res.Changed
}
