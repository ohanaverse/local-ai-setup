// Package litellm owns wt's management of LiteLLM's config.yaml: which
// registry models have a model_list route, how each row is built, and
// restarting the proxy after a change. It ports modelman's litellm.py.
package litellm

// Policy describes how one registry provider maps onto a LiteLLM row.
//   - Prefix     — LiteLLM `model` prefix (verbatim model string when FixedModel).
//   - APIKey     — literal api_key to write, "" to omit.
//   - SecretRef  — api_key comes from the provider's auth.secret_ref instead.
//   - Cloud      — the model lives remotely: exempt from the ready gate.
type Policy struct {
	Prefix     string
	FixedModel bool
	APIKey     string
	SecretRef  bool
	Cloud      bool
}

// policies is the single source of truth for provider exposure rules
// (modelman consults it through `wt litellm providers`). Native providers
// are deliberately absent: they never route through LiteLLM.
var policies = map[string]Policy{
	"ollama":        {Prefix: "ollama_chat/"},
	"omlx":          {Prefix: "openai/", APIKey: "not-needed"},
	"mlx_lm_server": {Prefix: "openai/", APIKey: "not-needed"},
	"mtplx":         {Prefix: "openai/", APIKey: "not-needed"},
	"llamacpp":      {Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"},
	"openrouter":    {Prefix: "openrouter/", SecretRef: true, Cloud: true},
}

// PolicyFor returns the mapping for providerID.
func PolicyFor(providerID string) (Policy, bool) {
	p, ok := policies[providerID]
	return p, ok
}
