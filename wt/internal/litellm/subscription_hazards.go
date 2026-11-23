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
	// AnthropicParams are the names (never the values) of the
	// litellm_params keys starting "anthropic_", sorted: per-deployment
	// settings LiteLLM's anthropic provider reads (wif.py's
	// anthropic_federation_* and the like).
	AnthropicParams []string
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
// "anthropic" when litellm_params.custom_llm_provider, the model's prefix
// before "/" or model_info.litellm_provider says so; otherwise it is what
// custom_llm_provider or the prefix names, which are what LiteLLM routes on
// ("" when neither names one: SubscriptionHazards then judges the deployment
// by its model). model_info.litellm_provider is only ever a signal for
// anthropic: a hand-set model_info overrides it on /model/info, and LiteLLM
// does not route on it, so it never makes a deployment another provider's.
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
		for k := range r.Params {
			if strings.HasPrefix(strings.ToLower(k), "anthropic_") {
				d.AnthropicParams = append(d.AnthropicParams, k)
			}
		}
		slices.Sort(d.AnthropicParams)
		// A credential name that is not a string (masked into another
		// shape) still names one.
		if v, ok := r.Params["litellm_credential_name"]; ok && v != nil && !d.Credential {
			if _, isString := v.(string); !isString {
				d.Credential = true
			}
		}
		d.Provider = custom
		if d.Provider == "" {
			d.Provider = prefix
		}
		if strings.EqualFold(custom, "anthropic") || strings.EqualFold(prefix, "anthropic") || strings.EqualFold(listed, "anthropic") {
			d.Provider = "anthropic"
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
			hazards = append(hazards, "config.yaml could not be read to check the proxy's settings")
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
		// The proxy loads its config from a bucket instead of the
		// config.yaml checked here (proxy_server.py get_config).
		if env.IsSet("LITELLM_CONFIG_BUCKET_NAME") {
			hazards = append(hazards, fmt.Sprintf("LITELLM_CONFIG_BUCKET_NAME is set in the proxy's environment (%s): the proxy loads its config from a bucket, not the config.yaml wt checks", env.Source))
		}
		// The proxy loads this file instead of its --config one
		// (proxy_server.py startup). WORKER_CONFIG is not checked: the
		// proxy CLI always overwrites it before the server reads it.
		if env.IsSet("CONFIG_FILE_PATH") {
			hazards = append(hazards, fmt.Sprintf("CONFIG_FILE_PATH is set in the proxy's environment (%s): the proxy loads that file, not the config.yaml wt checks", env.Source))
		}
	}
	if f != nil {
		for _, section := range []string{"general_settings", "litellm_settings"} {
			if forwardsHeaders(aliasValue(mergedGet(f.root(), section)), mergeDepth) {
				hazards = append(hazards, fmt.Sprintf("config.yaml turns on forward_client_headers_to_llm_api under %s: the proxy's master key could be forwarded to Anthropic", section))
			}
		}
		hazards = append(hazards, configCredentialHazards(f.root())...)
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

// configCredentialHazards are config.yaml's other ways to hand a keyless
// anthropic/ row a credential or an address (LiteLLM 1.109.1,
// proxy_server.py and main.py), each named, never valued:
//   - a credential or address variable named in the top-level
//     environment_variables block, which the proxy copies into its own
//     environment at load;
//   - litellm_settings.api_base, api_key or anthropic_key, which the proxy
//     sets as module globals and the anthropic dispatch falls back to for a
//     row that has none (wt's keyless row has none of them);
//   - general_settings.key_management_system: the secret manager is asked
//     for ANTHROPIC_API_KEY (and the rest) before the environment, so any
//     configured one fails closed.
//   - general_settings.use_azure_key_vault with any value but false (null
//     included: LiteLLM tests `is not False`) and a truthy
//     general_settings.use_google_kms, the deprecated switches that install
//     a secret manager the same way;
//   - a duplicated mapping key anywhere (wt reads the first copy, LiteLLM's
//     PyYAML the last, so a duplicate could hide any other check), named with
//     its line;
//   - any YAML construct wt does not evaluate the way LiteLLM's PyYAML does
//     (yamlConstructs: anchors, aliases, merge keys, an include key of the
//     root mapping, explicit tags). Rather than emulate PyYAML's merges and
//     YAML 1.1 rules, the check refuses them: wt's own writer emits no
//     anchor, alias, merge key, tag or root include (a registry model_info
//     key it copies into a row may be named include; LiteLLM reads include
//     only from the root, so a nested one is not counted);
//   - a document nested deeper than maxYAMLDepth levels: the walks stop
//     there, so what lies below would go unseen (fail closed).
func configCredentialHazards(root *yaml.Node) []string {
	var hazards []string
	for _, k := range duplicateKeys(root) {
		hazards = append(hazards, fmt.Sprintf("config.yaml has a duplicate key %s (line %d): the proxy reads the last copy, which wt does not check", oneLineString(k.Value), k.Line))
	}
	if nestsDeeperThan(root, maxYAMLDepth) {
		hazards = append(hazards, fmt.Sprintf("config.yaml nests deeper than %d levels, which wt does not check", maxYAMLDepth))
	}
	if c := yamlConstructs(root); len(c) > 0 {
		hazards = append(hazards, fmt.Sprintf("config.yaml uses %s, which wt does not evaluate the way LiteLLM does", strings.Join(c, ", ")))
	}
	if ev := aliasValue(mergedGet(root, "environment_variables")); ev != nil && ev.Kind == yaml.MappingNode {
		for _, name := range append(subscriptionCredentialVars(), subscriptionAddressVars()...) {
			if mergedGet(ev, name) != nil {
				hazards = append(hazards, fmt.Sprintf("config.yaml's environment_variables names %s, which the proxy copies into its environment: a keyless anthropic/ row would take it", name))
			}
		}
	}
	ls := aliasValue(mergedGet(root, "litellm_settings"))
	for _, key := range []string{"api_base", "api_key", "anthropic_key"} {
		if v := aliasValue(mergedGet(ls, key)); v != nil && !isNull(v) {
			hazards = append(hazards, fmt.Sprintf("config.yaml sets litellm_settings.%s, which a keyless anthropic/ row falls back to: it would bill a host credential or send the client's login elsewhere", key))
		}
	}
	gs := aliasValue(mergedGet(root, "general_settings"))
	if v := aliasValue(mergedGet(gs, "key_management_system")); v != nil && !isNull(v) {
		hazards = append(hazards, "config.yaml sets general_settings.key_management_system: the proxy asks that secret manager for Anthropic credentials before its environment, so a keyless anthropic/ row could take one")
	}
	if v := aliasValue(mergedGet(gs, "use_azure_key_vault")); v != nil && !plainFalse(v) {
		hazards = append(hazards, "config.yaml sets general_settings.use_azure_key_vault: the proxy asks Azure Key Vault for Anthropic credentials before its environment, so a keyless anthropic/ row could take one")
	}
	if v := aliasValue(mergedGet(gs, "use_google_kms")); v != nil && !plainOff(v) {
		hazards = append(hazards, "config.yaml sets general_settings.use_google_kms: the proxy asks Google KMS for Anthropic credentials before its environment, so a keyless anthropic/ row could take one")
	}
	return hazards
}

// duplicateKeys walks every mapping of the document under root and returns
// the second copy of each key that one mapping holds more than once (merge
// keys are left to yamlConstructs), in the order found. Alias nodes are not
// followed: the anchor they point at is walked where it is defined.
func duplicateKeys(root *yaml.Node) []*yaml.Node {
	var out []*yaml.Node
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		if n == nil || n.Kind == yaml.AliasNode || depth > maxYAMLDepth {
			return
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind == yaml.ScalarNode && !isMergeKey(k) {
					if seen[k.Value] && !slices.ContainsFunc(out, func(o *yaml.Node) bool { return o.Value == k.Value }) {
						out = append(out, k)
					}
					seen[k.Value] = true
				}
			}
		}
		for _, c := range n.Content {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	return out
}

// isMergeKey reports whether k is a YAML merge key (<<).
func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && (k.Tag == "!!merge" || k.Value == "<<" && k.Style == 0)
}

// yamlConstructs names, in a fixed order, the YAML constructs under root
// whose meaning wt does not evaluate the way LiteLLM's PyYAML does: anchors,
// aliases, merge keys, an include key of the root mapping (LiteLLM reads
// include only there and merges the included files; wt does not follow
// them; a root merge that could bring one in is already a merge key) and
// explicit tags (! or !!). Each one is a subscription hazard: wt's own
// config.yaml writer emits none of them.
func yamlConstructs(root *yaml.Node) []string {
	found := map[string]bool{}
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		if n == nil || depth > maxYAMLDepth {
			return
		}
		if n.Anchor != "" {
			found["anchors"] = true
		}
		if n.Kind == yaml.AliasNode {
			found["aliases"] = true
			return
		}
		if n.Style&yaml.TaggedStyle != 0 {
			found["tags"] = true
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				if isMergeKey(k) {
					found["merge keys"] = true
				}
				if depth == 0 && k.Kind == yaml.ScalarNode && k.Value == "include" {
					found["include"] = true
				}
			}
		}
		for _, c := range n.Content {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	var out []string
	for _, name := range []string{"anchors", "aliases", "merge keys", "include", "tags"} {
		if found[name] {
			out = append(out, name)
		}
	}
	return out
}

// maxYAMLDepth is how deep the hazard walks go into config.yaml; a document
// nested deeper is a hazard of its own (nestsDeeperThan).
const maxYAMLDepth = 64

// nestsDeeperThan reports whether a node under root (aliases not followed)
// lies deeper than limit levels below it.
func nestsDeeperThan(root *yaml.Node, limit int) bool {
	var walk func(n *yaml.Node, depth int) bool
	walk = func(n *yaml.Node, depth int) bool {
		if n == nil || n.Kind == yaml.AliasNode {
			return false
		}
		if depth > limit {
			return true
		}
		for _, c := range n.Content {
			if walk(c, depth+1) {
				return true
			}
		}
		return false
	}
	return walk(root, 0)
}

// plainFalse reports whether n is a plain (unquoted, untagged) scalar that
// both yaml.v3 and LiteLLM's PyYAML read as the boolean false: false, False
// or FALSE.
func plainFalse(n *yaml.Node) bool {
	return plainScalar(n) && (n.Value == "false" || n.Value == "False" || n.Value == "FALSE")
}

// plainOff reports whether n is off for a setting LiteLLM reads by Python
// truthiness: a plain scalar spelled false, False, FALSE, 0, null, ~ or
// empty. Every other spelling is on (fail closed): YAML 1.1 (PyYAML) and
// 1.2 (yaml.v3) disagree on others, such as 0o0 and 0e0 (zero to yaml.v3,
// truthy strings to PyYAML).
func plainOff(n *yaml.Node) bool {
	if !plainScalar(n) {
		return false
	}
	switch n.Value {
	case "false", "False", "FALSE", "0", "null", "~", "":
		return true
	}
	return false
}

// plainScalar reports whether n is a scalar written without quotes, block
// style or an explicit tag.
func plainScalar(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode &&
		n.Style&(yaml.TaggedStyle|yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) == 0
}

// oneLineString folds s onto one line (whitespace runs, newlines included,
// become one space): the proxy's own text, a model_name or a key, never
// starts a second output line.
func oneLineString(s string) string { return strings.Join(strings.Fields(s), " ") }

// deploymentHazard is the one hazard deployment d is for a keyless
// anthropic/ row, "" for none (SubscriptionHazards' deployment rules).
func deploymentHazard(d DeploymentInfo) string {
	name := d.ModelName
	if name == "" {
		name = d.Model
	}
	name = oneLineString(name)
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
	case len(d.AnthropicParams) > 0:
		return fmt.Sprintf("the proxy has an anthropic deployment %s with %s in its litellm_params: per-deployment anthropic settings can give it a credential or an address", name, oneLineString(strings.Join(d.AnthropicParams, ", ")))
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
