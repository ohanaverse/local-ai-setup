// Package windows answers one question: how many tokens of context does a
// model have, for the places that must say so (a gateway slot, a LiteLLM
// route). It imports only config and tiers.
package windows

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// Model returns m's context window in tokens, 0 when unknown. The registry's
// context_window wins. Without one, a cloud model takes the tiers file's
// window when it was read from the model's own host row
// (tiers.ContextBasisHost; a "largest_row" window is not one any host
// serves). A local model never takes a window from tiers, because the window
// a local server serves is whatever it was started with. A location that does
// not resolve reads as 0.
func Model(cfg *config.Config, m config.Model, ix tiers.Index) int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	loc, err := cfg.ResolveLocation(m)
	if err != nil || loc != config.LocationCloud {
		return 0
	}
	if e, ok := ix.Lookup(m.ProviderID, m.ModelName); ok && e.ContextBasis == tiers.ContextBasisHost {
		return e.Context
	}
	return 0
}
