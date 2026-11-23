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
// hide every other check. The hazard names the key and its line.
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
				if strings.Contains(l, "duplicate") && strings.Contains(l, tc.key) && strings.Contains(l, "(line ") {
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
}

// TestSubscriptionHazardsYAMLConstructs pins that config.yaml using any YAML
// construct wt does not evaluate the way LiteLLM's PyYAML does (an anchor, an
// alias, a merge key even once, an include at any depth, an explicit tag) is
// itself a hazard naming the construct, never a value: emulating PyYAML's
// merge and YAML 1.1 rules is how settings slipped past the check. The
// repeated-merge cases that hid a setting are each covered; a stream of two
// documents stays the unreadable-file hazard.
func TestSubscriptionHazardsYAMLConstructs(t *testing.T) {
	for _, tc := range []struct{ name, body, construct string }{
		{"anchor", "model_list: []\nbase: &b\n  x: 1\n", "anchors"},
		{"alias", "model_list: []\nbase: &b\n  x: 1\nother: *b\n", "aliases"},
		{"one merge key", "model_list: []\nbase: &b\n  x: 1\ngeneral_settings:\n  <<: *b\n", "merge keys"},
		{"repeated merges in litellm_settings", "model_list: []\na: &a\n  api_key: null\nb: &b\n  api_key: wtSENTINEL\nlitellm_settings:\n  <<: *a\n  <<: *b\n", "merge keys"},
		{"azure false then true", "model_list: []\na: &a\n  use_azure_key_vault: false\nb: &b\n  use_azure_key_vault: true\ngeneral_settings:\n  <<: *a\n  <<: *b\n", "merge keys"},
		{"kms null then set", "model_list: []\na: &a\n  key_management_system: null\nb: &b\n  key_management_system: wtSENTINEL\ngeneral_settings:\n  <<: *a\n  <<: *b\n", "merge keys"},
		{"root merge with litellm_settings", "x: &x\n  litellm_settings:\n    api_key: wtSENTINEL\n<<: *x\nmodel_list: []\n", "merge keys"},
		{"root merged include", "x: &x\n  include: [more.yaml]\n<<: *x\nmodel_list: []\n", "merge keys"},
		{"null include", "model_list: []\ninclude: null\n", "include"},
		{"explicit tag", "model_list: []\ngeneral_settings:\n  use_google_kms: !!str 0\n", "tags"},
		{"local tag", "model_list: []\ngeneral_settings:\n  master_key: !secret m\n", "tags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := rawHazards(t, tc.body)
			found := false
			for _, l := range h {
				if strings.Contains(l, tc.construct) && strings.Contains(l, "which wt does not evaluate the way LiteLLM does") {
					found = true
				}
				if strings.Contains(l, "SENTINEL") {
					t.Errorf("a hazard quotes a value: %q", l)
				}
			}
			if !found {
				t.Errorf("hazards = %q; want one naming %s", h, tc.construct)
			}
		})
	}
	h := rawHazards(t, "model_list: []\n---\nlitellm_settings:\n  api_key: x\n")
	if len(h) != 1 || h[0] != "config.yaml could not be read to check the proxy's settings" {
		t.Errorf("two documents: hazards = %q, want the unreadable-file hazard", h)
	}
}

// TestSubscriptionHazardsInclude pins that a top-level include is a hazard:
// LiteLLM merges the included files over config.yaml, and wt does not follow
// them, so anything could hide there. Any include key of the root mapping is
// one, empty, null or quoted included. LiteLLM reads include only from the
// root, so a nested include (an OpenAI extra_body include, a registry
// model_info key wt copies into a row) is not one.
func TestSubscriptionHazardsInclude(t *testing.T) {
	for _, body := range []string{"model_list: []\ninclude:\n  - more.yaml\n", "model_list: []\ninclude: null\n", "model_list: []\ninclude: []\n", "model_list: []\n\"include\": x.yaml\n"} {
		if h := rawHazards(t, body); len(h) != 1 || !strings.Contains(h[0], "include") {
			t.Errorf("%q: hazards = %q; want one naming include", body, h)
		}
	}
	for _, body := range []string{
		"model_list:\n  - model_name: m\n    litellm_params:\n      model: openai/m\n      extra_body:\n        include: [reasoning.encrypted_content]\n",
		"model_list:\n  - model_name: m\n    litellm_params:\n      model: openai/m\n    model_info:\n      include: [x]\n",
		"model_list: []\nlitellm_settings:\n  include: more.yaml\n",
	} {
		if h := rawHazards(t, body); len(h) != 0 {
			t.Errorf("%q: hazards = %q; want none for a nested include", body, h)
		}
	}
}

// TestSubscriptionHazardsDeepNesting pins that a document nested deeper than
// the walks' cap is a hazard of its own (fail closed): a construct, a
// duplicate or anything else below the cap would otherwise go unseen.
func TestSubscriptionHazardsDeepNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString("model_list: []\ndeep:\n")
	for i := 1; i <= 66; i++ {
		b.WriteString(strings.Repeat(" ", i) + "k:\n")
	}
	b.WriteString(strings.Repeat(" ", 67) + "a: 1\n" + strings.Repeat(" ", 67) + "a: 2\n")
	h := rawHazards(t, b.String())
	found := false
	for _, l := range h {
		if strings.Contains(l, "config.yaml nests deeper than 64 levels") {
			found = true
		}
	}
	if !found {
		t.Errorf("hazards = %q; want the nesting hazard", h)
	}
}

// TestSubscriptionHazardsLegacySecretManagers pins the two deprecated
// general_settings switches that install a secret manager LiteLLM asks for
// credentials before its environment: use_azure_key_vault (any value but
// PyYAML's False enables it, null included) and use_google_kms (truthy).
// use_google_kms is off only for a plain scalar PyYAML reads as falsy and wt
// reads the same way: false/False/FALSE, 0, null, ~ or empty. YAML 1.1 and
// 1.2 disagree on everything else (0o0 and 0e0 are zero to yaml.v3, truthy
// strings to PyYAML), so any other spelling is a hazard.
func TestSubscriptionHazardsLegacySecretManagers(t *testing.T) {
	for _, v := range []string{"true", "null", "\"\"", "0", "\"false\"", "no", "off"} {
		if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_azure_key_vault: "+v+"\n"); len(h) != 1 || !strings.Contains(h[0], "use_azure_key_vault") {
			t.Errorf("use_azure_key_vault: %s: hazards = %q, want one", v, h)
		}
	}
	for _, v := range []string{"false", "False", "FALSE"} {
		if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_azure_key_vault: "+v+"\n"); len(h) != 0 {
			t.Errorf("use_azure_key_vault: %s: hazards = %q, want none", v, h)
		}
	}
	for _, v := range []string{"true", "0o0", "0e0", "0.0", "yes", "no", "on", "off", "\"false\"", "\"0\"", "1", "[]"} {
		if h := rawHazards(t, "model_list: []\ngeneral_settings:\n  use_google_kms: "+v+"\n"); len(h) != 1 || !strings.Contains(h[0], "use_google_kms") {
			t.Errorf("use_google_kms: %s: hazards = %q, want one", v, h)
		}
	}
	for _, v := range []string{"false", "False", "FALSE", "0", "null", "~", ""} {
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
	// A registry model_info table wt copies into its row may hold any key,
	// include among them; it is not LiteLLM's root include.
	for i := range cfg.Models {
		if cfg.Models[i].ID == "openrouter/contract-fixture:cloud" {
			cfg.Models[i].ModelInfo = map[string]any{"include": []any{"x"}, "supports_function_calling": true}
		}
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
	if !strings.Contains(string(body), "anthropic/claude-haiku-5-5") || !strings.Contains(string(body), "include") {
		t.Fatalf("the sync did not route the subscription model:\n%s", body)
	}
	if h := rawHazards(t, string(body)); len(h) != 0 {
		t.Errorf("wt's own config.yaml has hazards %q:\n%s", h, body)
	}
}

// TestSubscriptionHazardsConfigFilePath pins that CONFIG_FILE_PATH in the
// proxy's environment is a hazard by its presence: LiteLLM then loads that
// file instead of the --config one wt checks.
func TestSubscriptionHazardsConfigFilePath(t *testing.T) {
	h := hazardsOf(t, map[string]string{"CONFIG_FILE_PATH": "wtSENTINEL"})
	if len(h) != 1 || !strings.Contains(h[0], "CONFIG_FILE_PATH") || strings.Contains(h[0], "SENTINEL") {
		t.Errorf("hazards = %q; want one naming the variable", h)
	}
}
