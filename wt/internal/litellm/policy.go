// Package litellm owns wt's management of LiteLLM's config.yaml: which
// registry models have a model_list route, how each row is built, and
// restarting the proxy after a change.
package litellm

// Policy describes how one registry provider maps onto a LiteLLM row.
//   - Prefix     — LiteLLM `model` prefix (verbatim model string when FixedModel).
//   - APIKey     — literal api_key to write, "" to omit.
//   - SecretRef  — api_key comes from resolving the provider's
//     auth.secret_ref (config.ResolveSecret — env, exec:, or
//     literal) instead.
//   - Cloud      — the model lives remotely (`wt litellm providers` reports it).
//   - V1Base     — OpenAI-compatible server: api_base is the provider's
//     origin plus "/v1" whichever form the registry stores, because
//     LiteLLM's openai/ provider appends only /chat/completions (#168).
type Policy struct {
	Prefix     string
	FixedModel bool
	APIKey     string
	SecretRef  bool
	Cloud      bool
	V1Base     bool
}

// policies is the single source of truth for provider exposure rules
// (`wt litellm providers` prints it). Native providers
// are deliberately absent: they never route through LiteLLM.
var policies = map[string]Policy{
	"ollama": {Prefix: "ollama_chat/"},
	"omlx":   {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
	// omlx-6bit is the same physical oMLX server serving the 6-bit variants,
	// so it maps exactly like omlx (each provider's own registry base_url is
	// what the row dials). Without an entry here a started 6-bit model got no
	// route at all and its stale rows survived the family sweep on stop.
	"omlx-6bit":     {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
	"mlx_lm_server": {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
	"mtplx":         {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
	"llamacpp":      {Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"},
	"openrouter":    {Prefix: "openrouter/", SecretRef: true, Cloud: true},
}

// PolicyFor returns the mapping for providerID.
func PolicyFor(providerID string) (Policy, bool) {
	p, ok := policies[providerID]
	return p, ok
}
