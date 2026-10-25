package lifecycle

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// Tenancy is how a provider's server holds models, which decides what a start
// displaces, what a stop takes down, and which routes each removes. The type
// and its values are internal/provider's, so the table and this engine share one
// vocabulary; see provider.Tenancy for what each value means.
type Tenancy = provider.Tenancy

const (
	// NoTenancy: wt has no lifecycle backend for the provider.
	NoTenancy = provider.NoTenancy
	// Exclusive: one model per process (mtplx).
	Exclusive = provider.Exclusive
	// Shared: models load beside each other (ollama).
	Shared = provider.Shared
	// Pool: several models under a memory ceiling (omlx).
	Pool = provider.Pool
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
