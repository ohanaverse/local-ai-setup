package catalog

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// subRows is one subscription row, one ollama row, an alias whose order-1
// target is the subscription row and an alias of the ollama row alone.
func subRows() []modelapi.Row {
	sub := modelapi.Row{ID: "claude-haiku-5-5", GatewayID: "claude-haiku-5-5", ProviderID: config.SubscriptionProvider,
		Location: "cloud", Order: 0, Registered: true, Action: modelapi.ActionLaunch, Tags: []string{"code"}}
	oll := modelapi.Row{ID: "ollama/x", GatewayID: "ollama/x", ProviderID: "ollama", Location: "cloud", Order: 1,
		Registered: true, Action: modelapi.ActionLaunch, Tags: []string{"code"}}
	group := aliasRow("alias/haiku", "cloud", 2,
		modelapi.AliasTarget{ID: "claude-haiku-5-5", Order: 1, ProviderID: config.SubscriptionProvider, Location: "cloud"},
		modelapi.AliasTarget{ID: "ollama/x", Order: 2, ProviderID: "ollama", Location: "cloud"})
	plain := aliasRow("alias/x", "cloud", 3,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama", Location: "cloud"})
	return []modelapi.Row{sub, oll, group, plain}
}

// TestSubscriptionRowsOnlyForClaude pins decision 6 and Global Constraints:
// a subscription row, and an alias with any subscription target, reach only
// the claude agent. Neither SupportedProviders naming claude-subscription nor
// AllProviders admits it for pi, codex, opencode or copilot, because a
// non-claude agent cannot send a claude.ai login and would spend the owner's
// subscription with the wrong credentials (or leak it to another tool).
func TestSubscriptionRowsOnlyForClaude(t *testing.T) {
	withSub := []string{"claude-haiku-5-5", "ollama/x", "alias/haiku", "alias/x"}
	without := []string{"ollama/x", "alias/x"}
	for _, c := range []struct {
		name  string
		agent config.Agent
		want  []string
	}{
		{"claude explicit", config.Agent{Name: "claude", SupportedProviders: []string{config.SubscriptionProvider, "ollama"}}, withSub},
		{"claude without the provider listed", config.Agent{Name: "claude", SupportedProviders: []string{"ollama"}}, withSub},
		{"claude AllProviders", config.Agent{Name: "claude", AllProviders: true}, withSub},
		{"pi AllProviders", config.Agent{Name: "pi", AllProviders: true}, without},
		{"pi naming claude-subscription", config.Agent{Name: "pi", SupportedProviders: []string{config.SubscriptionProvider, "ollama"}}, without},
		{"codex", config.Agent{Name: "codex", SupportedProviders: []string{config.SubscriptionProvider, "ollama"}, AllProviders: true}, without},
		{"opencode", config.Agent{Name: "opencode", SupportedProviders: []string{config.SubscriptionProvider, "ollama"}, AllProviders: true}, without},
		{"copilot", config.Agent{Name: "copilot", SupportedProviders: []string{config.SubscriptionProvider, "ollama"}, AllProviders: true}, without},
	} {
		if got := wid(EligibleWire(subRows(), c.agent, "", "")); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNonNativeModelsDropsSubscriptionRows pins that a gateway syncer's list
// (pi's models.json) never carries a subscription row or an alias with a
// subscription target: pi cannot use them, and a listed id invites a request
// that would spend the subscription without a claude.ai login.
func TestNonNativeModelsDropsSubscriptionRows(t *testing.T) {
	var ids []string
	for _, m := range NonNativeModels(subRows()) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"ollama/x", "alias/x"}) {
		t.Errorf("NonNativeModels = %v, want [ollama/x alias/x]", ids)
	}
}

// TestRotationSkipsSubscriptionRows pins that a no-pin launch never rotates
// onto a subscription row: rotation serves every agent, and only claude may
// use one (the user names a subscription model with -M or the picker).
func TestRotationSkipsSubscriptionRows(t *testing.T) {
	var ids []string
	for _, m := range RegisteredOrdered(subRows()) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"ollama/x"}) {
		t.Errorf("rotation list = %v, want [ollama/x]", ids)
	}
}

// TestUsesSubscription pins the one predicate a session uses to tell a
// subscription row: its own provider, or any alias target's provider, is
// the subscription provider. Anything else, an empty row included, is not.
func TestUsesSubscription(t *testing.T) {
	rows := subRows()
	for _, c := range []struct {
		name string
		row  modelapi.Row
		want bool
	}{
		{"subscription model", rows[0], true},
		{"ollama model", rows[1], false},
		{"alias with a subscription target", rows[2], true},
		{"alias without one", rows[3], false},
		{"alias with the subscription target second", aliasRow("alias/y", "cloud", 4,
			modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama"},
			modelapi.AliasTarget{ID: "claude-opus-5-5", Order: 2, ProviderID: config.SubscriptionProvider}), true},
		{"empty row", modelapi.Row{}, false},
	} {
		if got := UsesSubscription(c.row); got != c.want {
			t.Errorf("%s: UsesSubscription = %t, want %t", c.name, got, c.want)
		}
	}
}

// TestToModelSetsSubscription pins that the model a session launches knows
// whether it uses the subscription (decision 7): a subscription row and an
// alias with a subscription target set Model.Subscription, an ordinary row
// and an alias without one do not. The claude driver picks header mode from
// it; if it regresses a subscription launch goes out in token mode (a 401),
// or an ordinary one in header mode.
func TestToModelSetsSubscription(t *testing.T) {
	want := map[string]bool{"claude-haiku-5-5": true, "ollama/x": false, "alias/haiku": true, "alias/x": false}
	for _, r := range subRows() {
		if got := ToModel(r).Subscription; got != want[r.ID] {
			t.Errorf("ToModel(%s).Subscription = %v, want %v", r.ID, got, want[r.ID])
		}
	}
}
