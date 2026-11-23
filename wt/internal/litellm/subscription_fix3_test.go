package litellm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// rawHazards runs SubscriptionHazards over config.yaml exactly as body (no
// model_list prepended), a clean environment and no deployment.
func rawHazards(t *testing.T, body string) []string {
	t.Helper()
	p := writeConfig(t, body)
	none := func(context.Context) ([]DeploymentInfo, error) { return nil, nil }
	hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", nil), none)
	if err != nil {
		t.Fatal(err)
	}
	return hazards
}

// TestSubscriptionHazardsDuplicateKeys pins that a duplicated mapping key
// anywhere in config.yaml is a hazard naming the key, never a value: wt reads
// the first copy, LiteLLM (PyYAML safe_load) the last, so a duplicate could
// hide every other check. A merge key (<<) repeated is legal YAML and is not
// a duplicate.
func TestSubscriptionHazardsDuplicateKeys(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"litellm_settings api_base", "model_list: []\nlitellm_settings:\n  api_base: null\n  api_base: http://wtSENTINEL.example\n", "api_base"},
		{"two environment_variables blocks", "model_list: []\nenvironment_variables:\n  X: y\nenvironment_variables:\n  ANTHROPIC_API_KEY: wtSENTINEL\n", "environment_variables"},
		{"two general_settings blocks", "model_list: []\ngeneral_settings:\n  master_key: m\ngeneral_settings:\n  key_management_system: wtSENTINEL\n", "general_settings"},
		{"forward headers", "model_list: []\ngeneral_settings:\n  forward_client_headers_to_llm_api: false\n  forward_client_headers_to_llm_api: true\n", "forward_client_headers_to_llm_api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := rawHazards(t, tc.body)
			found := false
			for _, l := range h {
				if strings.Contains(l, "duplicate") && strings.Contains(l, tc.key) {
					found = true
				}
				if strings.Contains(l, "SENTINEL") {
					t.Errorf("a hazard quotes a value: %q", l)
				}
			}
			if !found {
				t.Errorf("hazards = %q; want one naming the duplicated %s", h, tc.key)
			}
		})
	}
	merges := "model_list: []\nbase: &b\n  x: 1\nmore: &m\n  y: 2\ngeneral_settings:\n  <<: *b\n  <<: *m\n  master_key: k\n"
	if h := rawHazards(t, merges); len(h) != 0 {
		t.Errorf("repeated merge keys: hazards = %q, want none", h)
	}
}

// TestSubscriptionHazardsInclude pins that a top-level include is a hazard:
// LiteLLM merges the included files over config.yaml, and wt does not follow
// them, so anything could hide there. An empty or null include is none.
func TestSubscriptionHazardsInclude(t *testing.T) {
	h := rawHazards(t, "model_list: []\ninclude:\n  - more.yaml\n")
	if len(h) != 1 || !strings.Contains(h[0], "include") {
		t.Errorf("hazards = %q; want one naming include", h)
	}
	for _, body := range []string{"model_list: []\ninclude: null\n", "model_list: []\ninclude: []\n"} {
		if h := rawHazards(t, body); len(h) != 0 {
			t.Errorf("%q: hazards = %q, want none", body, h)
		}
	}
}

// TestSubscriptionHazardsLegacySecretManagers pins the two deprecated
// general_settings switches that install a secret manager LiteLLM asks for
// credentials before its environment: use_azure_key_vault (any value but
// false enables it, null included) and use_google_kms (any truthy value).
func TestSubscriptionHazardsLegacySecretManagers(t *testing.T) {
	for _, v := range []string{"true", "null", "\"\"", "0"} {
		if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_azure_key_vault: "+v+"\n"); len(h) != 1 || !strings.Contains(h[0], "use_azure_key_vault") {
			t.Errorf("use_azure_key_vault: %s: hazards = %q, want one", v, h)
		}
	}
	if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_azure_key_vault: false\n"); len(h) != 0 {
		t.Errorf("use_azure_key_vault: false: hazards = %q, want none", h)
	}
	if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_google_kms: true\n"); len(h) != 1 || !strings.Contains(h[0], "use_google_kms") {
		t.Errorf("use_google_kms: true: hazards = %q, want one", h)
	}
	for _, v := range []string{"false", "null", "0", "\"\""} {
		if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_google_kms: "+v+"\n"); len(h) != 0 {
			t.Errorf("use_google_kms: %s: hazards = %q, want none", v, h)
		}
	}
}

// TestSubscriptionHazardsConfigBucket pins that LITELLM_CONFIG_BUCKET_NAME in
// the proxy's environment is a hazard: the proxy then loads its config from
// a bucket, not the config.yaml wt checks.
func TestSubscriptionHazardsConfigBucket(t *testing.T) {
	h := hazardsOf(t, map[string]string{"LITELLM_CONFIG_BUCKET_NAME": "wtSENTINEL"})
	if len(h) != 1 || !strings.Contains(h[0], "LITELLM_CONFIG_BUCKET_NAME") || strings.Contains(h[0], "SENTINEL") {
		t.Errorf("hazards = %q; want one naming the variable", h)
	}
}

// TestSubscriptionHazardLinesAreOneLine pins that a deployment's model_name
// and its anthropic_* key names reach a hazard line on one line: they are the
// proxy's text, and a newline in one would forge a second output line.
func TestSubscriptionHazardLinesAreOneLine(t *testing.T) {
	for _, d := range []DeploymentInfo{
		{Provider: "anthropic", ModelName: "a\nwt-model: forged", Model: "anthropic/x", APIBase: "https://relay.example"},
		{Provider: "anthropic", ModelName: "b", Model: "anthropic/x", AnthropicParams: []string{"anthropic_x\nforged"}},
	} {
		h := hazardsOf(t, nil, d)
		if len(h) != 1 || strings.ContainsAny(h[0], "\n\r") {
			t.Errorf("hazards = %q; want one single-line hazard", h)
		}
	}
	p := writeConfig(t, ": [")
	hazards, _, _ := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", nil), func(context.Context) ([]DeploymentInfo, error) { return nil, nil })
	if len(hazards) != 1 || hazards[0] != "config.yaml could not be read to check the proxy's settings" {
		t.Errorf("unreadable config.yaml: hazards = %q", hazards)
	}
}

// TestWtsOwnConfigHasNoSubscriptionHazard pins that the config.yaml wt itself
// writes (a sync of the shared sample registry) trips none of the config
// checks: no duplicate key, no include, no secret manager, no forwarding.
// A false positive here would keep every owner's subscription unrouted.
func TestWtsOwnConfigHasNoSubscriptionHazard(t *testing.T) {
	sample, err := filepath.Abs(filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", sample)
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("PINNED_CLOUD_API_KEY", "sk-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	o, _, p := opts(t, "model_list: []\n")
	o = cleanProxy(o)
	if _, err := Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "anthropic/claude-haiku-5-5") {
		t.Fatalf("the sync did not route the subscription model:\n%s", body)
	}
	if h := rawHazards(t, string(body)); len(h) != 0 {
		t.Errorf("wt's own config.yaml has hazards %q:\n%s", h, body)
	}
}
