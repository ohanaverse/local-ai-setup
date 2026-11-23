package litellm

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// configHazards runs SubscriptionHazards over config.yaml body with a clean
// environment and no deployment.
func configHazards(t *testing.T, body string) []string {
	t.Helper()
	p := writeConfig(t, "model_list: []\n"+body)
	none := func(context.Context) ([]DeploymentInfo, error) { return nil, nil }
	hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", nil), none)
	if err != nil {
		t.Fatal(err)
	}
	return hazards
}

// TestSubscriptionHazardsConfigEnvironmentVariables pins that config.yaml's
// top-level environment_variables block, which LiteLLM 1.109.1 copies into
// the proxy's os.environ at load (ProxyConfig._load_environment_variables),
// is checked like the proxy's environment: any credential or address
// variable named there is one hazard naming the variable and the block,
// never its value; an unrelated variable is none.
func TestSubscriptionHazardsConfigEnvironmentVariables(t *testing.T) {
	for _, name := range append(subscriptionCredentialVars(), subscriptionAddressVars()...) {
		t.Run(name, func(t *testing.T) {
			h := configHazards(t, "environment_variables:\n  "+name+": wtSENTINEL\n")
			if len(h) != 1 || !strings.Contains(h[0], name) || !strings.Contains(h[0], "environment_variables") || strings.Contains(h[0], "SENTINEL") {
				t.Errorf("hazards = %q; want one naming %s in environment_variables, no value", h, name)
			}
		})
	}
	if h := configHazards(t, "environment_variables:\n  ARIZE_ENDPOINT: https://otlp.example\n"); len(h) != 0 {
		t.Errorf("unrelated variable: hazards = %q", h)
	}
}

// TestSubscriptionHazardsLitellmSettingsGlobals pins the litellm_settings
// keys LiteLLM sets as module globals that its anthropic dispatch falls back
// to when a row has none (api_key, anthropic_key, api_base): wt's keyless row
// has no api_base and no key, so each would bill a host key or send the
// login elsewhere. Each is one hazard naming the key, never its value.
func TestSubscriptionHazardsLitellmSettingsGlobals(t *testing.T) {
	for _, key := range []string{"api_base", "api_key", "anthropic_key"} {
		h := configHazards(t, "litellm_settings:\n  drop_params: true\n  "+key+": wtSENTINEL\n")
		if len(h) != 1 || !strings.Contains(h[0], "litellm_settings."+key) || strings.Contains(h[0], "SENTINEL") {
			t.Errorf("%s: hazards = %q; want one naming litellm_settings.%s", key, h, key)
		}
	}
}

// TestSubscriptionHazardsKeyManagementSystem pins that a configured secret
// manager is a hazard: LiteLLM asks it for ANTHROPIC_API_KEY (and the other
// credentials) before the environment, so a clean environment proves nothing.
func TestSubscriptionHazardsKeyManagementSystem(t *testing.T) {
	h := configHazards(t, "general_settings:\n  key_management_system: hashicorp_vault\n")
	if len(h) != 1 || !strings.Contains(h[0], "key_management_system") || strings.Contains(h[0], "hashicorp") {
		t.Errorf("hazards = %q; want one naming key_management_system, no value", h)
	}
}

// TestSubscriptionHazardsListedProviderNeverClearsClaudeRule pins that
// model_info.litellm_provider (which a hand-set model_info overrides on
// /model/info, and which LiteLLM does not route on) never makes a
// provider-less claude model or wildcard "another provider's": only
// custom_llm_provider or the model's own provider/ prefix can.
func TestSubscriptionHazardsListedProviderNeverClearsClaudeRule(t *testing.T) {
	fake, admin := newFakeDB(t)
	fake.seed(DBModel{ID: "1", ModelName: "sneaky", Params: map[string]any{"model": "claude-3-5-sonnet", "api_base": "http://other.example"},
		Info: map[string]any{"litellm_provider": "openai"}})
	fake.seed(DBModel{ID: "2", ModelName: "*", Params: map[string]any{"model": "*"}, Info: map[string]any{"litellm_provider": "openai"}})
	deps, err := admin.ModelInfo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if h := hazardsOf(t, nil, d); len(h) != 1 {
			t.Errorf("%+v: hazards = %q, want one", d, h)
		}
	}
	other := DeploymentInfo{Provider: "openai", ModelName: "c", Model: "openai/claude-proxy", APIBase: "http://other.example"}
	if h := hazardsOf(t, nil, other); len(h) != 0 {
		t.Errorf("an explicit other provider: hazards = %q, want none", h)
	}
}

// TestSubscriptionHazardsAnthropicParams pins that an anthropic deployment
// carrying any litellm_params key starting "anthropic_" (wif.py reads the
// workload-identity settings per deployment, anthropic_federation_rule_id and
// the like) is a hazard naming the key, never its value; the reader carries
// key names only.
func TestSubscriptionHazardsAnthropicParams(t *testing.T) {
	fake, admin := newFakeDB(t)
	fake.seed(DBModel{ID: "1", ModelName: "wif", Params: map[string]any{"model": "anthropic/claude-x", "anthropic_federation_rule_id": "wtSENTINEL"}})
	deps, err := admin.ModelInfo(t.Context())
	if err != nil || len(deps) != 1 || !slices.Equal(deps[0].AnthropicParams, []string{"anthropic_federation_rule_id"}) {
		t.Fatalf("ModelInfo = %+v, %v", deps, err)
	}
	h := hazardsOf(t, nil, deps[0])
	if len(h) != 1 || !strings.Contains(h[0], "anthropic_federation_rule_id") || strings.Contains(h[0], "SENTINEL") {
		t.Errorf("hazards = %q; want one naming the key, no value", h)
	}
}

// TestApplyChangeRefusesASubscriptionAdd pins that a targeted route write
// (a start or stop's ApplyChange) never adds a subscription row: it runs no
// hazard check, so the row would go live unchecked. The add is dropped with
// one warning; the change's other adds still land.
func TestApplyChangeRefusesASubscriptionAdd(t *testing.T) {
	cfg := subscriptionConfig()
	o, _, p := opts(t, "model_list: []\n")
	res, err := ApplyChange(cfg, Change{Add: localFor(cfg, subID, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if rows := routedIDs(t, p); slices.Contains(rows, subID) || !slices.Contains(rows, "ollama/gemma:9b") {
		t.Errorf("rows = %v; want the ollama row and no subscription row", rows)
	}
	if ws := subscriptionWarnings(res.Warnings); len(ws) != 1 || !strings.Contains(ws[0], "sync") {
		t.Errorf("warnings = %q; want one saying a sync routes subscription models", ws)
	}
}
