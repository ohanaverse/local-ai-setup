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

// routeAfterStart adds the started model's LiteLLM route. A single-model
// provider (omlx, mtplx) can serve only one model, so starting one replaced
// whatever ran before: its siblings' routes are removed in the same write.
// It never fails the caller — a missing route is a warning, not a failed start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target) {
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
	applyAndReport(ctx, cfg, []string{m.ID}, remove)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every one of its models
// loses its route. modelName may be "" for a provider-wide stop.
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID, modelName string) {
	var remove []string
	switch {
	case SingleModel(providerID):
		remove = familyModelIDs(cfg, providerID)
	default:
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			return
		}
		remove = []string{m.ID}
	}
	applyAndReport(ctx, cfg, nil, remove)
}

// routeAfterOccupantStopped removes the route of the occupant a replace just
// stopped. It is the env hook defaultEnv wires in (env.go): the start that
// follows may still fail, and a failed Start runs no route hook at all, so the
// removal has to happen at the moment the occupant went down.
func routeAfterOccupantStopped(ctx context.Context, cfg *config.Config, occ localmodels.Entry) {
	routeAfterStop(ctx, cfg, occ.ProviderID, occ.ModelName)
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

func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string) {
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
		// The caller's ctx, so Ctrl+C during a start/stop does not keep
		// paying for the restart command's full run time.
		Restart: func() []string { return litellm.RestartContext(ctx) },
	})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return
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
	if res.Changed && wasUp {
		if err := waitProxy(ctx, url, proxyReadyTimeout); err != nil && ctx.Err() == nil { // cancelled by the user: stay quiet
			fmt.Fprintf(routesWarn, "wt: %v\n", err)
		}
	}
}
