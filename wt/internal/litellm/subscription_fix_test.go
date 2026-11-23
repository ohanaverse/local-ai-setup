package litellm

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// hazardsOf runs SubscriptionHazards over an empty config.yaml, the env vars
// and the deployments given.
func hazardsOf(t *testing.T, vars map[string]string, deps ...DeploymentInfo) []string {
	t.Helper()
	p := writeConfig(t, "model_list: []\n")
	info := func(context.Context) ([]DeploymentInfo, error) { return deps, nil }
	hazards, _, err := SubscriptionHazards(t.Context(), p, ProxyEnvOf("test env", vars), info)
	if err != nil {
		t.Fatal(err)
	}
	return hazards
}

// TestSubscriptionHazardsEveryAnthropicCredentialVariable pins every
// ANTHROPIC_* variable LiteLLM 1.109.1's anthropic provider reads as a
// credential or an address (llms/anthropic/common_utils.py and wif.py): any of
// them set in the proxy's environment gives a keyless anthropic/ row a
// credential of the host's (billed for any holder of the wt token) or sends
// the login elsewhere. Each is its own case; the hazard names the variable,
// never its value.
func TestSubscriptionHazardsEveryAnthropicCredentialVariable(t *testing.T) {
	credential := []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_FEDERATION_WORKSPACE_ID",
		"ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_SERVICE_ACCOUNT_ID",
		"ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_SOURCE",
	}
	for _, name := range credential {
		t.Run(name, func(t *testing.T) {
			h := hazardsOf(t, map[string]string{name: "wtSENTINEL"})
			if len(h) != 1 || !strings.Contains(h[0], name) || strings.Contains(h[0], "SENTINEL") ||
				!strings.Contains(h[0], "a keyless anthropic/ row would bill that credential for any holder of the wt token") {
				t.Errorf("hazards = %q; want one naming %s as a billed credential, no value", h, name)
			}
		})
	}
	for _, name := range []string{"ANTHROPIC_API_BASE", "ANTHROPIC_BASE_URL"} {
		t.Run(name, func(t *testing.T) {
			if h := hazardsOf(t, map[string]string{name: "wtSENTINEL"}); len(h) != 1 || !strings.Contains(h[0], name) || strings.Contains(h[0], "SENTINEL") {
				t.Errorf("hazards = %q; want one naming %s, no value", h, name)
			}
		})
	}
}

// TestSubscriptionHazardsProviderlessWildcardsAndClaudeRows pins that a
// deployment that names no provider is judged by its model: LiteLLM routes a
// provider-less claude-* model, and a "*" wildcard, to anthropic, so a
// provider-less wildcard (whatever its api_base) and a provider-less claude
// row on a foreign host are hazards, while a claude model behind another
// provider (openrouter/anthropic/...) and another provider's wildcard are not.
func TestSubscriptionHazardsProviderlessWildcardsAndClaudeRows(t *testing.T) {
	for _, d := range []DeploymentInfo{
		{Provider: "", ModelName: "*", Model: "*", APIBase: "https://corp-relay.example"},
		{Provider: "", ModelName: "claude-*", Model: "claude-*"},
		{Provider: "", ModelName: "c", Model: "Claude-x", APIBase: "https://corp-relay.example"},
		{Provider: "anthropic", ModelName: "anthropic/*", Model: "anthropic/*"},
	} {
		if h := hazardsOf(t, nil, d); len(h) != 1 {
			t.Errorf("%+v: hazards = %q, want one", d, h)
		}
	}
	for _, d := range []DeploymentInfo{
		{Provider: "openrouter", ModelName: "c", Model: "openrouter/anthropic/claude-x", APIBase: "https://openrouter.ai/api/v1"},
		{Provider: "openai", ModelName: "*", Model: "openai/*"},
		{Provider: "", ModelName: "local", Model: "gemma", APIBase: "http://localhost:11434"},
	} {
		if h := hazardsOf(t, nil, d); len(h) != 0 {
			t.Errorf("%+v: hazards = %q, want none", d, h)
		}
	}
}

// TestSubscriptionHazardsCredentialAndBaseURL pins the two other ways an
// anthropic deployment names its host: litellm_credential_name (the
// credential's api_base is applied at call time and never shown on
// /model/info, so naming one at all is a hazard) and base_url (another
// spelling of api_base). Both api_base and base_url must be exactly
// https://api.anthropic.com.
func TestSubscriptionHazardsCredentialAndBaseURL(t *testing.T) {
	for _, d := range []DeploymentInfo{
		{Provider: "anthropic", ModelName: "a", Model: "anthropic/claude-x", Credential: true},
		{Provider: "anthropic", ModelName: "b", Model: "anthropic/claude-x", BaseURL: "https://relay.example"},
		{Provider: "anthropic", ModelName: "c", Model: "anthropic/claude-x", APIBase: "https://api.anthropic.com/"},
	} {
		if h := hazardsOf(t, nil, d); len(h) != 1 || strings.Contains(h[0], "relay.example") {
			t.Errorf("%+v: hazards = %q, want one, no address", d, h)
		}
	}
	ok := DeploymentInfo{Provider: "anthropic", ModelName: "d", Model: "anthropic/claude-x", APIBase: anthropicAPIBase, BaseURL: anthropicAPIBase}
	if h := hazardsOf(t, nil, ok); len(h) != 0 {
		t.Errorf("api.anthropic.com: hazards = %q, want none", h)
	}
}

// TestAdminModelInfoReadsCredentialAndBaseURL pins the reader's two new
// fields: Credential is the presence of a non-empty litellm_credential_name
// (its value may be masked, so only presence counts) and BaseURL is
// litellm_params.base_url.
func TestAdminModelInfoReadsCredentialAndBaseURL(t *testing.T) {
	fake, admin := newFakeDB(t)
	fake.seed(DBModel{ID: "1", ModelName: "a", Params: map[string]any{"model": "anthropic/claude-x", "litellm_credential_name": "corp"}})
	fake.seed(DBModel{ID: "2", ModelName: "b", Params: map[string]any{"model": "anthropic/claude-x", "base_url": "https://relay.example"}})
	fake.seed(DBModel{ID: "3", ModelName: "c", Params: map[string]any{"model": "anthropic/claude-x", "litellm_credential_name": ""}})
	got, err := admin.ModelInfo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]DeploymentInfo{}
	for _, d := range got {
		byName[d.ModelName] = d
	}
	if !byName["a"].Credential || byName["b"].BaseURL != "https://relay.example" || byName["c"].Credential {
		t.Errorf("ModelInfo = %+v", got)
	}
}

// TestSubscriptionHazardsAmbientEnvFailsClosed pins that wt's own process
// environment never stands in for the proxy's: when the plist could not
// answer (a restart command is configured, or the plist is missing or
// unreadable), the proxy's real environment is unknown, so the check is one
// hazard even with no ANTHROPIC_* variable in sight, and a sync leaves the
// subscription row unrouted.
func TestSubscriptionHazardsAmbientEnvFailsClosed(t *testing.T) {
	ambient := ProxyEnv{Source: "wt's own environment", ambient: true}
	none := func(context.Context) ([]DeploymentInfo, error) { return nil, nil }
	p := writeConfig(t, "model_list: []\n")
	hazards, _, err := SubscriptionHazards(t.Context(), p, ambient, none)
	if err != nil || len(hazards) != 1 || !strings.Contains(hazards[0], "the proxy's environment could not be read (wt's own environment)") {
		t.Fatalf("hazards = %q, err %v; want the one could-not-be-read hazard", hazards, err)
	}
	o, _, cp := opts(t, "model_list: []\n")
	o = cleanProxy(o)
	o.ProxyEnv = func() ProxyEnv { return ambient }
	res, err := Sync(subscriptionConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(routedIDs(t, cp), subID) || len(subscriptionWarnings(res.Warnings)) != 1 {
		t.Errorf("rows %v warnings %q; want unrouted with one warning", routedIDs(t, cp), res.Warnings)
	}
}

// subAliasCfg is subscriptionConfig with alias/sub over the subscription model.
func subAliasCfg() *config.Config {
	cfg := subscriptionConfig()
	cfg.Aliases = []config.Alias{{ID: "alias/sub", Targets: []config.AliasTarget{{Model: subID, Order: 1}}}}
	return cfg
}

// TestBlockedGateDeletesAliasRowEvenWithAFaultyProviderRow pins that a seen
// hazard takes an alias's keyless anthropic/ row away even when the provider
// row is faulty too: the provider fault alone would freeze the alias row
// (phase 2), but a blocked gate must never leave a keyless row live, and the
// warning keeps the hazard's reason beside the fault.
func TestBlockedGateDeletesAliasRowEvenWithAFaultyProviderRow(t *testing.T) {
	reg := subAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o = cleanProxy(o)
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/sub"); !slices.Equal(got, []string{subID + "@1"}) {
		t.Fatalf("alias rows after a clean sync = %v", got)
	}
	bad := subAliasCfg()
	bad.Providers[len(bad.Providers)-1].Auth.BaseURL = "https://relay.example"
	reg = bad
	o.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": "wtSENTINEL"}) }
	res, err := Sync(bad, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); len(got) != 0 {
		t.Fatalf("alias rows = %v, want none under a blocked gate", got)
	}
	ws := subscriptionWarnings(res.Warnings)
	if len(ws) != 1 || !strings.Contains(ws[0], "ANTHROPIC_API_KEY") || !strings.Contains(ws[0], "base_url") {
		t.Errorf("subscription warnings = %q; want one naming the hazard and the fault", ws)
	}
}

// TestUnreadableProxyKeepsExistingSubscriptionRows pins the transient case:
// a sync whose only problem is that /model/info could not be read (a
// timeout, a 5xx) saw no hazard, so it keeps the subscription rows it has
// (config.yaml and alias rows: deleting them costs a proxy restart that drops
// every session's streams, then another) and adds none, with one warning. A
// sync with nothing yet routed still routes nothing (fail closed).
func TestUnreadableProxyKeepsExistingSubscriptionRows(t *testing.T) {
	reg := subAliasCfg()
	o, restarts, p, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", nil) }
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(routedIDs(t, p), subID) || len(fake.aliasIDs()) != 1 {
		t.Fatalf("clean sync: rows %v alias rows %v", routedIDs(t, p), fake.aliasIDs())
	}
	before, bounces := readFile(t, p), *restarts
	fake.mu.Lock()
	fake.failList = true
	fake.mu.Unlock()
	res, err := Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, p) != before || *restarts != bounces {
		t.Errorf("config.yaml changed or the proxy restarted on an unreadable /model/info")
	}
	if len(fake.aliasIDs()) != 1 {
		t.Errorf("alias rows = %v, want the old one kept", fake.aliasIDs())
	}
	if ws := subscriptionWarnings(res.Warnings); len(ws) != 1 {
		t.Errorf("subscription warnings = %q, want one", ws)
	}
	// A fresh config.yaml: nothing to keep, nothing added.
	o2, _, p2 := opts(t, "model_list: []\n")
	o2.ProxyEnv = o.ProxyEnv
	o2.ModelInfo = func(context.Context) ([]DeploymentInfo, error) { return nil, errors.New("503") }
	if _, err := Sync(subscriptionConfig(), nil, o2); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(routedIDs(t, p2), subID) {
		t.Errorf("rows = %v, want no new subscription row while unreadable", routedIDs(t, p2))
	}
}

// TestSubscriptionCheckReusesTheDatabaseListing pins that a sync with a
// database half lists /model/info once: the hazard check reads the listing
// the database half already made instead of asking the proxy again (one more
// request under the config.yaml lock, and two answers that could disagree).
func TestSubscriptionCheckReusesTheDatabaseListing(t *testing.T) {
	count := func(cfg *config.Config) int {
		reg := cfg
		o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
		o.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", nil) }
		if _, err := Sync(cfg, nil, o); err != nil {
			t.Fatal(err)
		}
		return fake.requests("GET /model/info")
	}
	without, with := count(testConfig()), count(subscriptionConfig())
	if with != without {
		t.Errorf("GET /model/info: %d with a subscription model, %d without; want the same", with, without)
	}
}

// TestNoSubscriptionModelNeverAsksTheProxy pins that a registry with no
// claude-subscription model never reads the proxy's environment or its
// deployments for the check (fakes that fail the test), and that Sync,
// PlanSync and DesiredIDs answer and write exactly as with no fakes at all:
// the check costs nothing to anyone who does not use the subscription.
func TestNoSubscriptionModelNeverAsksTheProxy(t *testing.T) {
	trip := func(o Options) Options {
		o.ProxyEnv = func() ProxyEnv { t.Fatal("ProxyEnv called without a subscription model"); return ProxyEnv{} }
		o.ModelInfo = func(context.Context) ([]DeploymentInfo, error) {
			t.Fatal("ModelInfo called without a subscription model")
			return nil, nil
		}
		return o
	}
	const body = "model_list:\n  - model_name: keep/me\n    litellm_params: {model: openai/gpt-4o}\n"
	run := func(o Options) (Result, string, SyncPlan, []string) {
		cfg := testConfig()
		plan, err := PlanSync(cfg, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := DesiredIDs(cfg, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Sync(cfg, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		return res, readFile(t, o.Path), plan, ids
	}
	a, _, _ := opts(t, body)
	b, _, _ := opts(t, body)
	r1, f1, p1, i1 := run(trip(a))
	r2, f2, p2, i2 := run(b)
	if !reflect.DeepEqual(r1, r2) || f1 != f2 || !reflect.DeepEqual(p1, p2) || !slices.Equal(i1, i2) {
		t.Errorf("results differ with the fakes:\n%+v\n%+v", r1, r2)
	}
}
