package lifecycle

import "github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"

// Tenancy is how a provider's server holds models, which decides what a start
// displaces, what a stop takes down, and which routes each removes.
type Tenancy int

const (
	// NoTenancy: wt has no lifecycle backend for the provider.
	NoTenancy Tenancy = iota
	// Exclusive: one model per process (mtplx). A start replaces the
	// occupant, a stop stops the process, and the family's routes go as one.
	Exclusive
	// Shared: models load beside each other and a pulled model is served on
	// request (ollama). Routes follow the artifact, not loaded state.
	Shared
	// Pool: several models loaded at once under a memory ceiling, evicted by
	// least recent use (omlx). A start loads beside and may evict; a stop
	// unloads one model; routes follow each model's loaded state.
	Pool
)

// TenancyOf reports how wt treats providerID's family. backendsByFamily is the
// single source of truth, so this cannot drift from the engine.
func TenancyOf(providerID string) Tenancy {
	b := backendsByFamily[localmodels.Family(providerID)]
	if b == nil {
		return NoTenancy
	}
	return b.tenancy()
}
