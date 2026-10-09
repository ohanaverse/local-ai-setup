// Package cloudsync is the pure core of `wt cloud-sync`: it turns what two
// public services publish into changes to registry.toml.
//
//   - Prices: OpenRouter's model list (ParseOpenRouter, PlanPrices) refreshes
//     the per-token prices of the registry's OpenRouter-priced models.
//   - Catalog: ollama.com/pricing (ParsePricing), each model's cloud tag
//     (ResolveCloudTags) and the plan that makes the registry's ollama cloud
//     entries mirror the page (PlanCatalog), with its two safety gates: the
//     mass-removal guard and the removal digest.
//
// Nothing here opens a socket, runs a command, prints or reads the clock. A
// page arrives as text, a fetch as a function the caller passes, the time as
// an argument. The two Apply methods change a config.RegistryDoc and nothing
// else, so they are safe inside config.UpdateRegistry, which may run them
// more than once. cmd/wt/cloudsync.go owns the fetches, the ollama CLI, the
// confirmation and the exit codes.
//
// ParsePricing is the only code that knows the pricing page's HTML shape:
// when ollama changes the page, `wt cloud-sync` exits 3 and saves the raw
// HTML, and the repair is there and in testdata/ollama_pricing.html.
package cloudsync
