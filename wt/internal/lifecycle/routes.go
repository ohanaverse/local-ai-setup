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
	routesWarn  io.Writer = os.Stderr
)

// proxyReadyTimeout bounds the wait for the proxy after a route change.
const proxyReadyTimeout = 30 * time.Second

// routeAfterStart adds the started model's LiteLLM route. A single-model
// provider (omlx, mtplx) can serve only one model, so starting one replaced
// whatever ran before: its siblings' routes are removed in the same write.
// It never fails the caller — a missing route is a warning, not a failed start.
func routeAfterStart(cfg *config.Config, t Target) {
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
	applyAndReport(cfg, []string{m.ID}, remove)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every one of its models
// loses its route. modelName may be "" for a provider-wide stop.
func routeAfterStop(cfg *config.Config, providerID, modelName string) {
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
	applyAndReport(cfg, nil, remove)
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

func applyAndReport(cfg *config.Config, add, remove []string) {
	res, err := applyRoutes(cfg, add, remove, litellm.Options{SkipReadyGate: true})
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
	for _, w := range res.Warnings {
		fmt.Fprintf(routesWarn, "wt: %s\n", w)
	}
	if res.Changed && cfg.LitellmBaseURL() != "" {
		if err := waitProxy(context.Background(), cfg.LitellmBaseURL(), proxyReadyTimeout); err != nil {
			fmt.Fprintf(routesWarn, "wt: %v\n", err)
		}
	}
}
