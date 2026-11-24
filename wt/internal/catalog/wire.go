package catalog

import (
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// wireAction maps the server's action to an Action. The wire is honoured as
// written, an unmapped cloud row's block included: the session has no direct
// launch path, so the server's block is authoritative.
func (r Row) wireAction() Action {
	switch r.Wire.Action {
	case modelapi.ActionStart:
		return ActionStart
	case modelapi.ActionBlock:
		return ActionBlock
	}
	return ActionLaunch
}

// ToModel converts a wire row to the model value the session works with.
func ToModel(r modelapi.Row) config.Model {
	m := config.Model{
		ID: r.ID, GatewayID: r.GatewayID, ProviderID: r.ProviderID, ModelName: r.ModelName,
		Family: r.Family, Tags: slices.Clone(r.Tags), Location: config.Location(r.Location), Native: r.Native,
		// A claude launch of it keeps the owner's login (header mode, #597).
		Subscription: UsesSubscription(r),
	}
	if r.Price != nil {
		m.Cost.InputPricePerMillion = r.Price.Input
		m.Cost.CachePricePerMillion = r.Price.Cache
		m.Cost.OutputPricePerMillion = r.Price.Output
	}
	// Every row's window: a cloud model's from the host's tiers.toml, any
	// model's own registry context_window, an alias's minimum over its
	// targets (0 when one is unknown). The server decides; claude's window
	// rules read it (#626).
	m.HostContext = r.Context
	if r.Context <= 0 && r.Kind == modelapi.KindAlias {
		for _, t := range r.Targets {
			if t.Context <= 0 {
				m.WindowLimiter = t.ID
				break
			}
		}
	}
	return m
}

// FromWire converts a wire row to a catalog Row that keeps the row it came
// from, so Ready, Action and BlockReason answer as the server decided.
func FromWire(r modelapi.Row) Row {
	row := Row{
		Model:      ToModel(r),
		Location:   config.Location(r.Location),
		Status:     StatusOK,
		Running:    r.State == modelapi.StateRunning || r.State == modelapi.StateLoading,
		Loading:    r.State == modelapi.StateLoading,
		Discovered: r.Discovered,
		Wire:       &r,
	}
	switch {
	case r.State == modelapi.StateUnknown:
		row.Status = StatusUnknown
	case r.Discovered:
		row.Status = StatusNew
	}
	return row
}

// EligibleWire is Config.EligibleModelsIn over wire rows: those the agent can
// use, filtered by tags and family, in Order. A sushi row reaches the agents
// in config.SushiAgents; an AllProviders agent takes every row but another
// agent's native one (the ProviderID == a.Name test relies on the convention
// that a native provider id equals its agent driver name: agy, claude,
// copilot). No protocol filter is applied (R5). An alias row (#596) is
// eligible when it has targets and every target's provider passes the same
// test (a target is never native: it is reached through the gateway), so a
// group never falls back onto a model the agent could not run itself.
func EligibleWire(rows []modelapi.Row, a config.Agent, tags, family string) []modelapi.Row {
	tagSet := config.ParseFilterList(tags)
	familySet := config.ParseFilterList(family)
	var out []modelapi.Row
	for _, r := range rows {
		ok := providerEligible(a, r.ProviderID, r.Native)
		if r.Kind == modelapi.KindAlias {
			ok = len(r.Targets) > 0 && !slices.ContainsFunc(r.Targets, func(t modelapi.AliasTarget) bool {
				return !providerEligible(a, t.ProviderID, false)
			})
		}
		if !ok {
			continue
		}
		if len(tagSet) > 0 && !slices.ContainsFunc(r.Tags, func(t string) bool { return slices.Contains(tagSet, t) }) {
			continue
		}
		if len(familySet) > 0 && !slices.Contains(familySet, r.Family) {
			continue
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(x, y modelapi.Row) int { return x.Order - y.Order })
	return out
}

// providerEligible is EligibleWire's provider test for one provider id. The
// subscription clause comes first (decision 6): a claude-subscription row
// reaches only config.SubscriptionAgents, whatever SupportedProviders or
// AllProviders say, so an alias with a subscription target is claude-only too.
func providerEligible(a config.Agent, providerID string, native bool) bool {
	if config.IsSubscriptionProvider(providerID) {
		return slices.Contains(config.SubscriptionAgents, a.Name)
	}
	return slices.Contains(a.SupportedProviders, providerID) ||
		(a.AllProviders && (!native || providerID == a.Name)) ||
		(providerID == "sushi" && slices.Contains(config.SushiAgents, a.Name))
}

// Launchable keeps the rows that can be launched as they are.
func Launchable(rows []modelapi.Row) []modelapi.Row {
	var out []modelapi.Row
	for _, r := range rows {
		if r.Action == modelapi.ActionLaunch {
			out = append(out, r)
		}
	}
	return out
}

// RegisteredOrdered is the registered rows as models, in Order: the list
// rotation walks. A discovered model is never in it, and neither is an alias
// row (decision 14: a launch with no pin never rotates onto an alias; the
// picker and -M alias/x list and launch one), nor a subscription row (#597:
// rotation serves every agent, and only claude may use the subscription).
func RegisteredOrdered(rows []modelapi.Row) []config.Model {
	var reg []modelapi.Row
	for _, r := range rows {
		if r.Registered && r.Kind != modelapi.KindAlias && !UsesSubscription(r) {
			reg = append(reg, r)
		}
	}
	slices.SortStableFunc(reg, func(x, y modelapi.Row) int { return x.Order - y.Order })
	out := make([]config.Model, 0, len(reg))
	for _, r := range reg {
		out = append(out, ToModel(r))
	}
	return out
}

// NonNativeModels is the rows a gateway syncer lists (pi's models.json): every
// row that is not a native model, as models, in the catalog's order. A row
// that uses the subscription (UsesSubscription) is left out: only claude may
// use one, and claude syncs no models file.
func NonNativeModels(rows []modelapi.Row) []config.Model {
	var out []config.Model
	for _, r := range rows {
		if !r.Native && !UsesSubscription(r) {
			out = append(out, ToModel(r))
		}
	}
	return out
}

// UsesSubscription reports whether a row is served on the owner's Claude
// subscription: its provider is config.SubscriptionProvider, or (an alias)
// any of its targets' is. The wire has no marker of its own (decision 6).
func UsesSubscription(r modelapi.Row) bool {
	return config.IsSubscriptionProvider(r.ProviderID) ||
		slices.ContainsFunc(r.Targets, func(t modelapi.AliasTarget) bool { return config.IsSubscriptionProvider(t.ProviderID) })
}

// NeedsStart reports whether the model must be started (or its route added)
// before a launch: the server says start, or a running local model the
// gateway is known not to list. An unknown route reads as false (R7).
func NeedsStart(r modelapi.Row) bool {
	if r.Action == modelapi.ActionStart {
		return true
	}
	return r.Location == string(config.LocationLocal) && r.State == modelapi.StateRunning && r.Routed != nil && !*r.Routed
}
