// Package litellm owns wt's management of LiteLLM's config.yaml: which
// registry models have a model_list route, how each row is built, and
// restarting the proxy after a change.
package litellm

import "github.com/ohanaverse/local-ai-setup/wt/internal/provider"

// Policy describes how one registry provider maps onto a LiteLLM row (see
// provider.Policy for the fields). The mappings are internal/provider's table,
// the single source of truth for provider exposure rules (`wt litellm
// providers` prints it). Native providers are deliberately absent: they never
// route through LiteLLM.
type Policy = provider.Policy

// PolicyFor returns the mapping for providerID.
func PolicyFor(providerID string) (Policy, bool) {
	return provider.PolicyFor(providerID)
}
