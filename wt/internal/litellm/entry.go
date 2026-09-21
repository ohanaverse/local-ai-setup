package litellm

import (
	"fmt"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

const tokensPerMillion = 1_000_000

// kv is one ordered mapping entry; val is a plain Go value or a *yaml.Node.
type kv struct {
	key string
	val any
}

// mapping builds an ordered YAML mapping node.
func mapping(pairs []kv) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, p := range pairs {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.key}, toNode(p.val))
	}
	return n
}

func toNode(v any) *yaml.Node {
	if n, ok := v.(*yaml.Node); ok {
		return n
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		// Encode only fails on unsupported Go types; registry values are
		// TOML scalars, so surface it loudly rather than writing a bad row.
		panic(fmt.Sprintf("litellm: cannot encode %T: %v", v, err))
	}
	return n
}

// pricingInfo derives model_info pricing keys from a registry cost: per-token
// prices from per-million, cache price on both cache keys, and explicit zeros
// when a price is absent so LiteLLM bypasses budget checks.
func pricingInfo(c config.ModelCost) []kv {
	perToken := func(p *float64) any {
		if p == nil {
			return 0
		}
		return *p / tokensPerMillion
	}
	info := []kv{
		{"input_cost_per_token", perToken(c.InputPricePerMillion)},
		{"output_cost_per_token", perToken(c.OutputPricePerMillion)},
	}
	if c.CachePricePerMillion != nil {
		v := *c.CachePricePerMillion / tokensPerMillion
		info = append(info, kv{"cache_creation_input_token_cost", v}, kv{"cache_read_input_token_cost", v})
	}
	return info
}

// BuildEntry builds the LiteLLM `model_list` row for a registry model:
// model_name is the registry id, litellm_params come from the provider
// policy, and model_info is derived pricing overridden by the model's own
// model_info keys (existing keys keep their position, new ones append in
// sorted order for stable output).
func BuildEntry(m config.Model, p config.Provider) (*yaml.Node, error) {
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	model := pol.Prefix
	if !pol.FixedModel {
		model = pol.Prefix + m.ModelName
	}
	params := []kv{{"model", model}}
	if p.Auth.BaseURL != "" {
		params = append(params, kv{"api_base", p.Auth.BaseURL})
	}
	switch {
	case pol.SecretRef:
		params = append(params, kv{"api_key", p.Auth.SecretRef})
	case pol.APIKey != "":
		params = append(params, kv{"api_key", pol.APIKey})
	}

	info := pricingInfo(m.Cost)
	extra := make([]string, 0, len(m.ModelInfo))
	for k := range m.ModelInfo {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		replaced := false
		for i := range info {
			if info[i].key == k {
				info[i].val, replaced = m.ModelInfo[k], true
				break
			}
		}
		if !replaced {
			info = append(info, kv{k, m.ModelInfo[k]})
		}
	}

	return mapping([]kv{
		{"model_name", m.ID},
		{"litellm_params", mapping(params)},
		{"model_info", mapping(info)},
	}), nil
}
