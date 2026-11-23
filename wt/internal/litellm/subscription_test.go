package litellm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// subID is the one subscription model the tests register: the bare real
// Claude id, registry id and model_name alike.
const subID = "claude-haiku-5-5"

// subscriptionConfig is testConfig plus the claude-subscription provider
// (seeded shape: client-login, no base_url, no secret_ref) and one model on
// it whose location is inherited from the provider (cloud).
func subscriptionConfig() *config.Config {
	cfg := testConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: provider.IDClaudeSubscription, Location: config.LocationCloud,
		Auth: config.AuthConfig{Type: "client-login"},
	})
	cfg.Models = append(cfg.Models, config.Model{
		ID: subID, ProviderID: provider.IDClaudeSubscription, ModelName: subID, Family: "claude",
	})
	return cfg
}

// cleanProxy is the Options a subscription sync reads the proxy through when
// nothing is wrong: an environment that sets nothing and a /model/info that
// lists one ordinary deployment.
func cleanProxy(o Options) Options {
	o.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", nil) }
	o.ModelInfo = func(context.Context) ([]DeploymentInfo, error) {
		return []DeploymentInfo{{Provider: "openrouter", ModelName: "openrouter/x/y", Model: "openrouter/x/y"}}, nil
	}
	return o
}

// subscriptionWarnings returns the warnings that are about subscription rows.
func subscriptionWarnings(ws []string) []string {
	var out []string
	for _, w := range ws {
		if strings.Contains(w, "Claude subscription") {
			out = append(out, w)
		}
	}
	return out
}

// routedIDs is the model_names of p's rows.
func routedIDs(t *testing.T, p string) []string {
	t.Helper()
	var ids []string
	for _, r := range readRows(t, p) {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestSubscriptionRowIsKeylessAnthropic pins the LiteLLM row of a
// subscription model: `anthropic/<id>` under the bare id, with no api_key and
// no api_base, so LiteLLM forwards the client's own login to
// api.anthropic.com and never a key of the host's; pricing is the explicit
// zero every unpriced row gets.
func TestSubscriptionRowIsKeylessAnthropic(t *testing.T) {
	cfg := subscriptionConfig()
	m := cfg.Models[len(cfg.Models)-1]
	node, err := BuildEntry(m, cfg.Providers[len(cfg.Providers)-1])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model_name":     subID,
		"litellm_params": map[string]any{"model": "anthropic/" + subID},
		"model_info": map[string]any{
			"input_cost_per_token":  0,
			"output_cost_per_token": 0,
			"wt_managed":            true,
		},
	}
	if got := decode(t, node); !reflect.DeepEqual(got, want) {
		t.Fatalf("row =\n%v\nwant\n%v", got, want)
	}
	if row, err := prepareModel(cfg, m); err != nil || row == nil {
		t.Fatalf("prepareModel(%s) = %v, %v; want the row", subID, row, err)
	}
}

// TestSubscriptionProviderWithBaseURLOrSecretRefused pins the three provider
// rows a subscription model is never routed through: one with auth.base_url
// (the login would go to that host), one with auth.secret_ref (a key of the
// host's would be sent instead of the login) and a native one. prepareModel
// refuses each; a sync leaves the model unrouted (an old wt row goes) with one
// warning; and an alias over it is frozen, phase 2's rule for a target whose
// row cannot be built.
func TestSubscriptionProviderWithBaseURLOrSecretRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth config.AuthConfig
	}{
		{"base_url", config.AuthConfig{Type: "client-login", BaseURL: "https://proxy.example"}},
		{"secret_ref", config.AuthConfig{Type: "client-login", SecretRef: "ANTHROPIC_API_KEY"}},
		{"native", config.AuthConfig{Type: "native"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := subscriptionConfig()
			cfg.Providers[len(cfg.Providers)-1].Auth = tc.auth
			m := cfg.Models[len(cfg.Models)-1]
			if _, err := prepareModel(cfg, m); err == nil {
				t.Fatalf("prepareModel accepted a subscription model on a provider with %s", tc.name)
			}
			o, _, p := opts(t, "model_list:\n  - model_name: "+subID+"\n    litellm_params: {model: anthropic/"+subID+"}\n    model_info: {wt_managed: true}\n")
			res, err := Sync(cfg, nil, cleanProxy(o))
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(routedIDs(t, p), subID) {
				t.Errorf("rows = %v, want %s unrouted", routedIDs(t, p), subID)
			}
			if ws := subscriptionWarnings(res.Warnings); len(ws) != 1 {
				t.Errorf("subscription warnings = %q, want exactly one", ws)
			}
			a := config.Alias{ID: "alias/sub", Targets: []config.AliasTarget{{Model: subID, Order: 1}}}
			rows, skips := DBModelsFromAlias(cfg, a, func(string) bool { return true }, "k")
			if len(rows) != 0 || len(skips) != 1 || !skips[0].Frozen {
				t.Errorf("alias over it: rows %d, skips %+v; want none and one frozen skip", len(rows), skips)
			}
		})
	}
}

// TestSubscriptionRowsUnroutedWhileProxyHasAnthropicKey pins the paid-key
// hazard: while the proxy's environment sets ANTHROPIC_API_KEY, a keyless
// anthropic/ row would answer any holder of the shared wt token on the host's
// API key, so the sync (and its dry run) leaves the subscription row unrouted
// (an existing wt row goes) with one warning naming the variable and never
// its value. The same registry with a clean environment routes it.
func TestSubscriptionRowsUnroutedWhileProxyHasAnthropicKey(t *testing.T) {
	const secret = "sk-ant-api03-wtSENTINELVALUE"
	keyed := func(o Options) Options {
		o = cleanProxy(o)
		o.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": secret}) }
		return o
	}
	cfg := subscriptionConfig()
	o, _, p := opts(t, "model_list:\n  - model_name: "+subID+"\n    litellm_params: {model: anthropic/"+subID+"}\n    model_info: {wt_managed: true}\n")
	plan, err := PlanSync(cfg, nil, keyed(o))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Add, subID) || !slices.Contains(plan.Remove, subID) {
		t.Errorf("dry run add %v remove %v; want %s removed, not added", plan.Add, plan.Remove, subID)
	}
	res, err := Sync(cfg, nil, keyed(o))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(routedIDs(t, p), subID) {
		t.Fatalf("rows = %v, want %s unrouted", routedIDs(t, p), subID)
	}
	ws := subscriptionWarnings(res.Warnings)
	if len(ws) != 1 || !strings.Contains(ws[0], "ANTHROPIC_API_KEY") {
		t.Fatalf("subscription warnings = %q, want one naming ANTHROPIC_API_KEY", ws)
	}
	for _, w := range append(res.Warnings, plan.Warnings...) {
		if strings.Contains(w, "SENTINEL") {
			t.Fatalf("a warning carries the key's value: %q", w)
		}
	}
	if !slices.Contains(routedIDs(t, p), "openrouter/x/y") {
		t.Errorf("rows = %v: the other cloud rows must still be routed", routedIDs(t, p))
	}
	if _, err := Sync(cfg, nil, cleanProxy(o)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(routedIDs(t, p), subID) {
		t.Errorf("rows = %v after a clean sync, want %s routed", routedIDs(t, p), subID)
	}
}

// TestSubscriptionHazards pins litellm.SubscriptionHazards and the sync that
// uses it: a foreign-host anthropic deployment (hand-written, no wt marker),
// an anthropic/* wildcard and client-header forwarding in config.yaml each
// are a hazard that leaves the subscription row unrouted, because LiteLLM
// forwards a client's login to every anthropic/ deployment and Claude Code
// can name any model_name; an anthropic/ row on https://api.anthropic.com is
// none; store_prompts_in_spend_logs is a warning only, and the row is routed.
func TestSubscriptionHazards(t *testing.T) {
	const base = "model_list: []\n"
	for _, tc := range []struct {
		name    string
		yaml    string
		info    []DeploymentInfo
		hazard  string // a word the one hazard names; "" for none
		warning string // a word the one warning names; "" for none
	}{
		{name: "foreign api_base", yaml: base,
			info:   []DeploymentInfo{{Provider: "anthropic", ModelName: "hand-claude", Model: "anthropic/claude-x", APIBase: "https://relay.example"}},
			hazard: "hand-claude"},
		{name: "wildcard", yaml: base,
			info:   []DeploymentInfo{{Provider: "anthropic", ModelName: "anthropic/*", Model: "anthropic/*"}},
			hazard: "wildcard"},
		{name: "forward headers general_settings", yaml: base + "general_settings:\n  forward_client_headers_to_llm_api: true\n",
			hazard: "forward_client_headers_to_llm_api"},
		{name: "forward headers litellm_settings", yaml: base + "litellm_settings:\n  forward_client_headers_to_llm_api: true\n",
			hazard: "forward_client_headers_to_llm_api"},
		{name: "anthropic on api.anthropic.com", yaml: base,
			info: []DeploymentInfo{{Provider: "anthropic", ModelName: "hand-claude", Model: "anthropic/claude-x", APIBase: "https://api.anthropic.com"}}},
		{name: "store prompts", yaml: base + "general_settings:\n  store_prompts_in_spend_logs: true\n",
			warning: "store_prompts_in_spend_logs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := func(context.Context) ([]DeploymentInfo, error) { return tc.info, nil }
			p := writeConfig(t, tc.yaml)
			hazards, warnings, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", nil), info)
			if err != nil {
				t.Fatal(err)
			}
			if tc.hazard == "" && len(hazards) != 0 || tc.hazard != "" && (len(hazards) != 1 || !strings.Contains(hazards[0], tc.hazard)) {
				t.Errorf("hazards = %q, want one naming %q (none when empty)", hazards, tc.hazard)
			}
			if tc.warning == "" && len(warnings) != 0 || tc.warning != "" && (len(warnings) != 1 || !strings.Contains(warnings[0], tc.warning)) {
				t.Errorf("warnings = %q, want one naming %q (none when empty)", warnings, tc.warning)
			}
			for _, s := range append(hazards, warnings...) {
				if strings.Contains(s, "relay.example") {
					t.Errorf("a line quotes an api_base value: %q", s)
				}
			}
			o, _, _ := opts(t, "")
			o.Path = p
			o = cleanProxy(o)
			o.ModelInfo = info
			res, err := Sync(subscriptionConfig(), nil, o)
			if err != nil {
				t.Fatal(err)
			}
			routed := slices.Contains(routedIDs(t, p), subID)
			if routed != (tc.hazard == "") {
				t.Errorf("routed = %v with hazard %q; rows %v", routed, tc.hazard, routedIDs(t, p))
			}
			if ws := subscriptionWarnings(res.Warnings); tc.hazard != "" && len(ws) != 1 {
				t.Errorf("subscription warnings = %q, want exactly one", ws)
			}
		})
	}
}

// TestSubscriptionHazardsFromTheProxyEnv pins the environment hazards on their
// own: ANTHROPIC_API_KEY (a paid key behind a keyless row), and the two
// variables that move LiteLLM's anthropic base (a login sent elsewhere), each
// named, never valued; an environment without them is no hazard.
func TestSubscriptionHazardsFromTheProxyEnv(t *testing.T) {
	none := func(context.Context) ([]DeploymentInfo, error) { return nil, nil }
	p := writeConfig(t, "model_list: []\n")
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_API_BASE", "ANTHROPIC_BASE_URL"} {
		hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", map[string]string{name: "wtSENTINEL"}), none)
		if err != nil || len(hazards) != 1 || !strings.Contains(hazards[0], name) || strings.Contains(hazards[0], "SENTINEL") {
			t.Errorf("%s: hazards = %q, err %v; want one naming it, no value", name, hazards, err)
		}
	}
	if hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", map[string]string{"OPENROUTER_API_KEY": "x"}), none); err != nil || len(hazards) != 0 {
		t.Errorf("unrelated variable: hazards = %q, err %v", hazards, err)
	}
}

// TestSubscriptionRowsUnroutedWhenModelInfoUnreadable pins the fail-closed
// rule: a sync that cannot list the proxy's deployments cannot rule out a
// foreign anthropic row, so it adds no subscription row (one warning), and
// the next sync that can list them routes it; a later unreadable sync keeps
// the row it has rather than restarting the proxy over a transient error. SubscriptionHazards
// itself returns the error (serve warns and keeps the mode on) but still
// reports the hazards it could see.
func TestSubscriptionRowsUnroutedWhenModelInfoUnreadable(t *testing.T) {
	broken := func(context.Context) ([]DeploymentInfo, error) { return nil, errors.New("connection refused") }
	cfg := subscriptionConfig()
	o, _, p := opts(t, "model_list: []\n")
	o = cleanProxy(o)
	good := o.ModelInfo
	o.ModelInfo = broken
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(routedIDs(t, p), subID) {
		t.Fatalf("rows = %v, want %s unrouted", routedIDs(t, p), subID)
	}
	if ws := subscriptionWarnings(res.Warnings); len(ws) != 1 {
		t.Fatalf("subscription warnings = %q, want exactly one", ws)
	}
	o.ModelInfo = good
	if _, err := Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(routedIDs(t, p), subID) {
		t.Fatalf("rows = %v after a readable sync, want %s routed", routedIDs(t, p), subID)
	}
	hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": "x"}), broken)
	if err == nil || len(hazards) != 1 {
		t.Errorf("SubscriptionHazards = %q, %v; want the env hazard and the read error", hazards, err)
	}
	// No ModelInfo at all (no proxy pair) is unreadable too: no hazard was
	// seen, so the row it has is kept (not deleted), with one warning.
	o.ModelInfo, o.Admin = nil, nil
	if res, err := Sync(cfg, nil, o); err != nil || !slices.Contains(routedIDs(t, p), subID) || len(subscriptionWarnings(res.Warnings)) != 1 {
		t.Errorf("no reader: err %v rows %v warnings %q; want the existing row kept, one warning", err, routedIDs(t, p), res.Warnings)
	}
}

// TestAliasOverSubscriptionFollowsItsRoute pins that an alias row over a
// subscription target exists only while the target's own row is routed: an
// alias row is a keyless anthropic/ row too, so the hazards that unroute the
// model must take the alias row with it (a skip that is not frozen, so an old
// row goes).
func TestAliasOverSubscriptionFollowsItsRoute(t *testing.T) {
	cfg := subscriptionConfig()
	a := config.Alias{ID: "alias/sub", Targets: []config.AliasTarget{{Model: subID, Order: 1}}}
	rows, skips := DBModelsFromAlias(cfg, a, func(string) bool { return false }, "k")
	if len(rows) != 0 || len(skips) != 1 || skips[0].Frozen {
		t.Fatalf("unrouted target: rows %d skips %+v; want no row and one skip that is not frozen", len(rows), skips)
	}
	rows, _ = DBModelsFromAlias(cfg, a, func(id string) bool { return id == subID }, "k")
	if len(rows) != 1 || rows[0].Params["model"] != "anthropic/"+subID {
		t.Fatalf("routed target: rows %+v; want one anthropic row", rows)
	}
	if _, ok := rows[0].Params["api_key"]; ok {
		t.Errorf("alias row carries an api_key: %+v", rows[0].Params)
	}
}

// TestAdminModelInfoReadsEveryDeployment pins the reader the hazard check
// lists the proxy with: every /model/info row (database and config.yaml rows,
// wt's and hand-written), with its provider judged from custom_llm_provider,
// the model prefix or model_info.litellm_provider (any of them saying
// anthropic makes it anthropic: a bare claude-* model is one too), its
// model_name and its api_base.
func TestAdminModelInfoReadsEveryDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" || r.Header.Get("Authorization") != "Bearer "+testMasterKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[
{"model_name":"a","litellm_params":{"model":"anthropic/claude-x","api_base":"https://relay.example"},"model_info":{"id":"1"}},
{"model_name":"b","litellm_params":{"model":"claude-bare"},"model_info":{"id":"2","litellm_provider":"anthropic"}},
{"model_name":"c","litellm_params":{"model":"openrouter/x/y","api_base":null},"model_info":{"id":"3","db_model":true}},
{"model_name":"d","litellm_params":{"model":"x","custom_llm_provider":"anthropic"},"model_info":{"id":"4"}}]}`))
	}))
	defer srv.Close()
	got, err := AdminFromPair(srv.URL, testMasterKey).ModelInfo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []DeploymentInfo{
		{Provider: "anthropic", ModelName: "a", Model: "anthropic/claude-x", APIBase: "https://relay.example"},
		{Provider: "anthropic", ModelName: "b", Model: "claude-bare"},
		{Provider: "openrouter", ModelName: "c", Model: "openrouter/x/y"},
		{Provider: "anthropic", ModelName: "d", Model: "x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelInfo = %+v\nwant %+v", got, want)
	}
}
