package litellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// A claude-subscription model (#597) is routed as a keyless anthropic/<id>
// row: LiteLLM forwards the client's own claude.ai login to Anthropic. That
// is safe only while nothing else in the proxy can take the login or put a
// key of the host's behind the row. LiteLLM forwards a client's OAuth token
// to every anthropic/ deployment whatever its api_base, and Claude Code can
// name any model_name, so the whole proxy is checked, not only wt's rows.
// SubscriptionHazards is that check; the sync leaves every subscription row
// unrouted while it finds a hazard or cannot look, and `wt-model serve`
// keeps the header mode off while it finds one.

// anthropicAPIBase is the one api_base an anthropic deployment may name.
const anthropicAPIBase = "https://api.anthropic.com"

// subscriptionCheckTimeout bounds the /model/info read of one sync's
// subscription check (serve's start check has its own bound).
const subscriptionCheckTimeout = 5 * time.Second

// DeploymentInfo is one deployment the proxy lists on /model/info, any
// owner's (config.yaml or database, wt's or hand-written): the provider
// LiteLLM routes it to, its model_name, its litellm_params.model and its
// api_base ("" when none is set).
type DeploymentInfo struct {
	Provider, ModelName, Model, APIBase string
}

// ModelInfoFunc lists the proxy's deployments: AdminClient.ModelInfo in a
// binary, a fake in tests.
type ModelInfoFunc func(ctx context.Context) ([]DeploymentInfo, error)

// ProxyEnvOf is a ProxyEnv that answers from vars alone (nil: nothing is
// set), with source naming where they came from. A caller that already knows
// the proxy's environment, and every test, builds one with it; production
// reads the real one with LoadProxyEnv.
func ProxyEnvOf(source string, vars map[string]string) ProxyEnv {
	m := make(map[string]string, len(vars))
	for k, v := range vars {
		m[k] = v
	}
	return ProxyEnv{Source: source, vars: m}
}

// ModelInfo lists every deployment the proxy serves (GET /model/info), not
// only the database rows List returns. A deployment's provider is
// "anthropic" when any of litellm_params.custom_llm_provider, the model's
// prefix before "/" and model_info.litellm_provider says so (a bare claude-*
// model is LiteLLM's anthropic too); otherwise the first of them that is set.
func (c *AdminClient) ModelInfo(ctx context.Context) ([]DeploymentInfo, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/model/info", nil, listBodyLimit)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.statusError("list models", status, body)
	}
	var resp struct {
		Data []struct {
			ModelName string         `json:"model_name"`
			Params    map[string]any `json:"litellm_params"`
			Info      map[string]any `json:"model_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("litellm: list models: bad response: %w", err)
	}
	out := make([]DeploymentInfo, 0, len(resp.Data))
	for _, r := range resp.Data {
		model, _ := r.Params["model"].(string)
		base, _ := r.Params["api_base"].(string)
		custom, _ := r.Params["custom_llm_provider"].(string)
		listed, _ := r.Info["litellm_provider"].(string)
		prefix := ""
		if p, _, ok := strings.Cut(model, "/"); ok {
			prefix = p
		}
		d := DeploymentInfo{ModelName: r.ModelName, Model: model, APIBase: base}
		for _, p := range []string{custom, prefix, listed} {
			if strings.EqualFold(p, "anthropic") {
				d.Provider = "anthropic"
				break
			}
			if d.Provider == "" {
				d.Provider = p
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// SubscriptionHazards checks the proxy for what makes a keyless anthropic/
// row unsafe (decision 5), worded without a value:
//   - env (the proxy's environment, ProxyEnv.IsSet) sets ANTHROPIC_API_KEY,
//     ANTHROPIC_API_BASE or ANTHROPIC_BASE_URL;
//   - the config.yaml at cfgPath ("" or a missing file: not read) turns on
//     forward_client_headers_to_llm_api in general_settings or
//     litellm_settings (at any depth: model_group_settings included), so the
//     master key could be forwarded to Anthropic; a file that cannot be read
//     or parsed is a hazard too;
//   - info lists an anthropic deployment whose api_base is set and is not
//     https://api.anthropic.com, or an anthropic wildcard (a "*" in its model
//     or model_name).
//
// store_prompts_in_spend_logs on is a warning, not a hazard. err is non-nil
// only when info could not be read (nil info included); the hazards the
// environment and config.yaml show are returned with it.
func SubscriptionHazards(ctx context.Context, cfgPath string, env ProxyEnv, info ModelInfoFunc) (hazards, warnings []string, err error) {
	var f *File
	if cfgPath != "" {
		opened, oerr := Open(cfgPath)
		switch {
		case oerr == nil:
			f = opened
		case errors.Is(oerr, ErrMissing):
		default:
			hazards = append(hazards, "config.yaml could not be read to check forward_client_headers_to_llm_api")
		}
	}
	h, w, err := subscriptionHazards(ctx, f, env, info)
	return append(hazards, h...), w, err
}

// subscriptionHazards is SubscriptionHazards over an open config.yaml (nil:
// none to check).
func subscriptionHazards(ctx context.Context, f *File, env ProxyEnv, info ModelInfoFunc) (hazards, warnings []string, err error) {
	// The variables that make a keyless anthropic/ row unsafe: a key of the
	// host's that LiteLLM would send for any caller without a login, and the
	// two that move LiteLLM's anthropic base away from api.anthropic.com
	// (where the login would then go).
	envVars := []struct{ name, why string }{
		{"ANTHROPIC_API_KEY", "a keyless anthropic/ row would bill that key for any holder of the wt token"},
		{"ANTHROPIC_API_BASE", "a keyless anthropic/ row would send the client's login to that address"},
		{"ANTHROPIC_BASE_URL", "a keyless anthropic/ row would send the client's login to that address"},
	}
	for _, v := range envVars {
		if env.IsSet(v.name) {
			hazards = append(hazards, fmt.Sprintf("%s is set in the proxy's environment (%s): %s", v.name, env.Source, v.why))
		}
	}
	if f != nil {
		for _, section := range []string{"general_settings", "litellm_settings"} {
			if forwardsHeaders(aliasValue(mergedGet(f.root(), section)), mergeDepth) {
				hazards = append(hazards, fmt.Sprintf("config.yaml turns on forward_client_headers_to_llm_api under %s: the proxy's master key could be forwarded to Anthropic", section))
			}
		}
		gs := aliasValue(mergedGet(f.root(), "general_settings"))
		if v := aliasValue(mergedGet(gs, "store_prompts_in_spend_logs")); v != nil && !isNull(v) && !isFalse(v) {
			warnings = append(warnings, "general_settings.store_prompts_in_spend_logs is on: the proxy keeps the prompts of Claude subscription requests in its spend logs")
		}
	}
	if info == nil {
		return hazards, warnings, errors.New("no LiteLLM proxy pair is set, so its deployments cannot be listed")
	}
	deps, ierr := info(ctx)
	if ierr != nil {
		return hazards, warnings, fmt.Errorf("the proxy's deployments could not be listed (%s)", oneLine(ierr))
	}
	for _, d := range deps {
		if !strings.EqualFold(d.Provider, "anthropic") {
			continue
		}
		name := d.ModelName
		if name == "" {
			name = d.Model
		}
		switch {
		case strings.Contains(d.Model, "*") || strings.Contains(d.ModelName, "*"):
			hazards = append(hazards, fmt.Sprintf("the proxy has an anthropic wildcard deployment (%s): it would take any Claude model name, and a client's login with it", name))
		case d.APIBase != "" && strings.TrimRight(d.APIBase, "/") != anthropicAPIBase:
			hazards = append(hazards, fmt.Sprintf("the proxy has an anthropic deployment %s whose api_base is not %s: LiteLLM forwards a client's login to every anthropic deployment", name, anthropicAPIBase))
		}
	}
	return hazards, warnings, nil
}

// forwardsHeaders reports whether n (a settings mapping) holds
// forward_client_headers_to_llm_api with any value other than false or null,
// at any depth up to depth (model_group_settings nests it).
func forwardsHeaders(n *yaml.Node, depth int) bool {
	if n == nil || n.Kind != yaml.MappingNode || depth < 0 {
		return false
	}
	if v := aliasValue(mergedGet(n, "forward_client_headers_to_llm_api")); v != nil && !isNull(v) && !isFalse(v) {
		return true
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if forwardsHeaders(aliasValue(n.Content[i+1]), depth-1) {
			return true
		}
	}
	return false
}

// isFalse reports whether n is the YAML literal false.
func isFalse(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!bool" && n.Value == "false"
}

// subscriptionGate is one write's answer about subscription rows: whether
// they may be routed and the warnings the answer owes (Sync and PlanSync
// print them). A nil gate on a registry with subscription models routes none.
type subscriptionGate struct {
	blocked  string // why every subscription row stays unrouted; "" routes them
	warnings []string
}

// subscriptionModels is the registry's subscription model ids.
func subscriptionModels(cfg *config.Config) []string {
	var ids []string
	for _, m := range cfg.Models {
		if config.IsSubscriptionProvider(m.ProviderID) && !slices.Contains(ids, m.ID) {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// gateSubscription runs the hazard check for one write over f (config.yaml as
// opened under the lock), with o's proxy environment (Options.ProxyEnv, else
// LoadProxyEnv) and deployment reader (Options.ModelInfo, else the proxy
// pair's AdminClient.ModelInfo, else none: fail closed), bounded by
// subscriptionCheckTimeout. nil when the registry has no subscription model:
// nothing is read then.
func gateSubscription(cfg *config.Config, f *File, o Options) *subscriptionGate {
	if len(subscriptionModels(cfg)) == 0 {
		return nil
	}
	env := LoadProxyEnv
	if o.ProxyEnv != nil {
		env = o.ProxyEnv
	}
	info := o.ModelInfo
	if info == nil && o.Admin != nil {
		info = o.Admin.ModelInfo
	}
	parent := o.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, subscriptionCheckTimeout)
	defer cancel()
	hazards, warnings, err := subscriptionHazards(ctx, f, env(), info)
	g := &subscriptionGate{warnings: warnings}
	switch {
	case len(hazards) > 0:
		g.blocked = strings.Join(hazards, "; ")
	case err != nil:
		g.blocked = err.Error() + "; the next sync that can list them routes them"
	}
	return g
}

// subscriptionProviderFault is why a claude-subscription provider row can
// never carry a route (decision 5): an auth.base_url (the login would go to
// that host), an auth.secret_ref (a key of the host's would be sent instead
// of the login) or auth.type native. nil for a usable row.
func subscriptionProviderFault(p config.Provider) error {
	switch {
	case p.Auth.Type == "native":
		return fmt.Errorf("provider %q is native; a Claude subscription provider row has auth.type client-login", p.ID)
	case p.Auth.BaseURL != "":
		return fmt.Errorf("provider %q has auth.base_url: a Claude subscription row is sent only to %s; remove the key", p.ID, anthropicAPIBase)
	case p.Auth.SecretRef != "":
		return fmt.Errorf("provider %q has auth.secret_ref: a Claude subscription row carries the client's own login, never a key; remove the key", p.ID)
	}
	return nil
}

// subscriptionUnrouted decides, for planSync, which subscription models stay
// unrouted and the one warning each reason owes: every one while the gate is
// blocked (or was never run), and the models of a provider row that can
// never carry a route. Their existing wt rows are removed by the plan.
func subscriptionUnrouted(cfg *config.Config, g *subscriptionGate) (map[string]bool, []string) {
	ids := subscriptionModels(cfg)
	if len(ids) == 0 {
		return nil, nil
	}
	out := map[string]bool{}
	var warnings []string
	if p := cfg.ProviderByID(config.SubscriptionProvider); p != nil {
		if err := subscriptionProviderFault(*p); err != nil {
			for _, id := range ids {
				out[id] = true
			}
			return out, []string{fmt.Sprintf("Claude subscription models (%s) are not routed: %s", strings.Join(ids, ", "), oneLine(err))}
		}
	}
	reason := ""
	switch {
	case g == nil:
		reason = "the proxy was not checked for subscription hazards"
	case g.blocked != "":
		reason = g.blocked
	}
	if g != nil {
		warnings = append(warnings, g.warnings...)
	}
	if reason != "" {
		for _, id := range ids {
			out[id] = true
		}
		warnings = append(warnings, fmt.Sprintf("Claude subscription models (%s) are not routed: %s", strings.Join(ids, ", "), reason))
	}
	return out, warnings
}

// PairedConfigPath is the config.yaml a check that only reads it may read:
// DefaultPath, or "" when the registry is redirected and nothing names
// config.yaml (the pairing route writes refuse, ErrRegistryRedirected), so a
// scratch registry is never checked against the real proxy's file.
func PairedConfigPath() string {
	if checkRegistryPairing(Options{}) != nil {
		return ""
	}
	return DefaultPath()
}
