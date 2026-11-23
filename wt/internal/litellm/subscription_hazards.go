package litellm

import (
	"context"
	"errors"
	"fmt"
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
	// BaseURL is litellm_params.base_url, another spelling of api_base.
	BaseURL string
	// Credential is set when litellm_params.litellm_credential_name is
	// present and not empty: the credential's api_base and key apply at call
	// time and /model/info never shows them (the name may be masked, so only
	// its presence counts).
	Credential bool
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
// only the database rows List returns (deploymentsOf).
func (c *AdminClient) ModelInfo(ctx context.Context) ([]DeploymentInfo, error) {
	rows, err := c.listRows(ctx)
	if err != nil {
		return nil, err
	}
	return deploymentsOf(rows), nil
}

// deploymentsOf reads a listing's deployments. A deployment's provider is
// "anthropic" when any of litellm_params.custom_llm_provider, the model's
// prefix before "/" and model_info.litellm_provider says so; otherwise the
// first of them that is set ("" when none is: SubscriptionHazards judges such
// a deployment by its model).
func deploymentsOf(rows []modelInfoRow) []DeploymentInfo {
	out := make([]DeploymentInfo, 0, len(rows))
	for _, r := range rows {
		model, _ := r.Params["model"].(string)
		base, _ := r.Params["api_base"].(string)
		baseURL, _ := r.Params["base_url"].(string)
		cred, _ := r.Params["litellm_credential_name"].(string)
		custom, _ := r.Params["custom_llm_provider"].(string)
		listed, _ := r.Info["litellm_provider"].(string)
		prefix := ""
		if p, _, ok := strings.Cut(model, "/"); ok {
			prefix = p
		}
		d := DeploymentInfo{ModelName: r.ModelName, Model: model, APIBase: base, BaseURL: baseURL, Credential: cred != ""}
		// A credential name that is not a string (masked into another
		// shape) still names one.
		if v, ok := r.Params["litellm_credential_name"]; ok && v != nil && !d.Credential {
			if _, isString := v.(string); !isString {
				d.Credential = true
			}
		}
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
	return out
}

// SubscriptionHazards checks the proxy for what makes a keyless anthropic/
// row unsafe (decision 5), worded without a value:
//   - env is wt's own environment standing in for the proxy's (the plist
//     could not answer): the proxy's real environment is unknown, one hazard;
//   - env (the proxy's environment, ProxyEnv.IsSet) sets an ANTHROPIC_*
//     variable LiteLLM 1.109.1's anthropic provider reads as a credential
//     (llms/anthropic/common_utils.py, wif.py) or an address;
//   - the config.yaml at cfgPath ("" or a missing file: not read) turns on
//     forward_client_headers_to_llm_api in general_settings or
//     litellm_settings (at any depth: model_group_settings included), so the
//     master key could be forwarded to Anthropic; a file that cannot be read
//     or parsed is a hazard too;
//   - info lists a wildcard deployment ("*" in its model or model_name) whose
//     provider is anthropic or none, or an anthropic deployment that names a
//     litellm credential (its address is never shown) or whose api_base or
//     base_url is set and is not exactly https://api.anthropic.com. A
//     deployment with no provider whose model starts with "claude" is
//     LiteLLM's anthropic too.
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

// subscriptionCredentialVars are the ANTHROPIC_* variables LiteLLM 1.109.1's
// anthropic provider reads as a credential when a row has no key of its own
// (common_utils.py's AnthropicModelInfo.validate_environment and the
// workload-identity federation of wif.py); subscriptionAddressVars the two it
// reads as the base address (common_utils.py's get_api_base).
func subscriptionCredentialVars() []string {
	return []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_FEDERATION_WORKSPACE_ID",
		"ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_SERVICE_ACCOUNT_ID",
		"ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_SOURCE",
	}
}

func subscriptionAddressVars() []string { return []string{"ANTHROPIC_API_BASE", "ANTHROPIC_BASE_URL"} }

// subscriptionHazards is SubscriptionHazards over an open config.yaml (nil:
// none to check).
func subscriptionHazards(ctx context.Context, f *File, env ProxyEnv, info ModelInfoFunc) (hazards, warnings []string, err error) {
	if env.ambient {
		// wt's own environment says nothing about the proxy's: never pass
		// on it (fail closed).
		hazards = append(hazards, fmt.Sprintf("the proxy's environment could not be read (%s); subscription rows are not routed", env.Source))
	} else {
		for _, name := range subscriptionCredentialVars() {
			if env.IsSet(name) {
				hazards = append(hazards, fmt.Sprintf("%s is set in the proxy's environment (%s): a keyless anthropic/ row would bill that credential for any holder of the wt token", name, env.Source))
			}
		}
		for _, name := range subscriptionAddressVars() {
			if env.IsSet(name) {
				hazards = append(hazards, fmt.Sprintf("%s is set in the proxy's environment (%s): a keyless anthropic/ row would send the client's login to that address", name, env.Source))
			}
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
		if h := deploymentHazard(d); h != "" {
			hazards = append(hazards, h)
		}
	}
	return hazards, warnings, nil
}

// deploymentHazard is the one hazard deployment d is for a keyless
// anthropic/ row, "" for none (SubscriptionHazards' deployment rules).
func deploymentHazard(d DeploymentInfo) string {
	name := d.ModelName
	if name == "" {
		name = d.Model
	}
	noProvider := d.Provider == ""
	anthropic := strings.EqualFold(d.Provider, "anthropic") ||
		noProvider && (strings.HasPrefix(strings.ToLower(d.Model), "claude") || strings.HasPrefix(strings.ToLower(d.ModelName), "claude"))
	wildcard := strings.Contains(d.Model, "*") || strings.Contains(d.ModelName, "*")
	switch {
	case wildcard && (anthropic || noProvider):
		return fmt.Sprintf("the proxy has a wildcard deployment (%s) that LiteLLM routes to anthropic: it would take any Claude model name, and a client's login with it", name)
	case !anthropic:
		return ""
	case d.Credential:
		return fmt.Sprintf("the proxy has an anthropic deployment %s that names a litellm credential, whose address /model/info does not show: LiteLLM forwards a client's login to every anthropic deployment", name)
	case d.APIBase != "" && d.APIBase != anthropicAPIBase:
		return fmt.Sprintf("the proxy has an anthropic deployment %s whose api_base is not exactly %s: LiteLLM forwards a client's login to every anthropic deployment", name, anthropicAPIBase)
	case d.BaseURL != "" && d.BaseURL != anthropicAPIBase:
		return fmt.Sprintf("the proxy has an anthropic deployment %s whose base_url is not exactly %s: LiteLLM forwards a client's login to every anthropic deployment", name, anthropicAPIBase)
	}
	return ""
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

// subscriptionGate is one write's answer about subscription rows (Sync,
// PlanSync and DesiredIDs print its warnings):
//   - blocked: a hazard was seen; every subscription row (model and alias)
//     goes, and none is added;
//   - hold: no hazard was seen but the proxy's deployments could not be
//     listed (a timeout, a 5xx, no proxy pair): the rows that exist are kept
//     (deleting them costs a proxy restart over a transient error) and none
//     is added;
//   - neither: the rows are routed.
//
// A nil gate (a write that ran no check) is hold.
type subscriptionGate struct {
	blocked  string
	hold     string
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
// LoadProxyEnv) and deployments: Options.ModelInfo when set; else the
// listing the write's database half db already made (one /model/info per
// write), or its failure; else, when db never listed (store_model_in_db
// off), db's AdminClient.ModelInfo; else none (no proxy pair): unreadable.
// It is bounded by db's deadline when there is a database half (the budget
// every API call of the write shares), else by subscriptionCheckTimeout. nil
// when the registry has no subscription model: nothing is read then.
func gateSubscription(cfg *config.Config, f *File, o Options, db *dbRoutes) *subscriptionGate {
	if len(subscriptionModels(cfg)) == 0 {
		return nil
	}
	env := LoadProxyEnv
	if o.ProxyEnv != nil {
		env = o.ProxyEnv
	}
	info := o.ModelInfo
	if info == nil && db != nil {
		switch {
		case db.listed:
			deps := db.deployments
			info = func(context.Context) ([]DeploymentInfo, error) { return deps, nil }
		case db.reason != "":
			reason := db.reason
			info = func(context.Context) ([]DeploymentInfo, error) { return nil, errors.New(reason) }
		default:
			info = db.admin.ModelInfo
		}
	}
	var ctx context.Context
	if db != nil {
		ctx = db.context()
	} else {
		parent := o.Ctx
		if parent == nil {
			parent = context.Background()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, subscriptionCheckTimeout)
		defer cancel()
	}
	hazards, warnings, err := subscriptionHazards(ctx, f, env(), info)
	g := &subscriptionGate{warnings: warnings}
	switch {
	case len(hazards) > 0:
		g.blocked = strings.Join(hazards, "; ")
	case err != nil:
		g.hold = err.Error()
	}
	return g
}

// aliasState is what the alias half does with a subscription target
// (dbModelsFromAlias), from the write's gate.
type aliasState int

const (
	aliasSubClean   aliasState = iota // build it as any target
	aliasSubHold                      // keep its rows as they are, add none (Frozen)
	aliasSubBlocked                   // its rows go (never Frozen)
)

// aliasStateOf is g's aliasState: blocked when a hazard was seen, hold when
// the check could not look or was not run (nil), clean otherwise.
func aliasStateOf(g *subscriptionGate) aliasState {
	switch {
	case g != nil && g.blocked != "":
		return aliasSubBlocked
	case g == nil || g.hold != "":
		return aliasSubHold
	}
	return aliasSubClean
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

// subscriptionUnrouted decides, for planSync, what happens to the
// subscription models and the one warning that owes: off are unrouted (wt's
// rows removed, none added) while the gate is blocked or the provider row
// can never carry a route (the gate's reason is kept beside the fault);
// hold are left as they are (kept, none added) while the gate could not
// look or was not run.
func subscriptionUnrouted(cfg *config.Config, g *subscriptionGate) (off, hold map[string]bool, warnings []string) {
	ids := subscriptionModels(cfg)
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if g != nil {
		warnings = append(warnings, g.warnings...)
	}
	var reasons []string
	if p := cfg.ProviderByID(config.SubscriptionProvider); p != nil {
		if err := subscriptionProviderFault(*p); err != nil {
			reasons = append(reasons, oneLine(err))
		}
	}
	if g != nil && g.blocked != "" {
		reasons = append(reasons, g.blocked)
	}
	list := strings.Join(ids, ", ")
	all := func() map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	switch {
	case len(reasons) > 0:
		return all(), nil, append(warnings, fmt.Sprintf("Claude subscription models (%s) are not routed: %s", list, strings.Join(reasons, "; ")))
	case g == nil:
		return nil, all(), append(warnings, fmt.Sprintf("Claude subscription models (%s) are not checked: the proxy was not checked for subscription hazards; their routes are left as they are and none is added", list))
	case g.hold != "":
		return nil, all(), append(warnings, fmt.Sprintf("Claude subscription models (%s) are not checked: %s; their routes are left as they are and none is added until a sync can list the proxy's deployments", list, g.hold))
	}
	return nil, nil, warnings
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
