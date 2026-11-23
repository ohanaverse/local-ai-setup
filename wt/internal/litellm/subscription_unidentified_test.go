package litellm

import (
	"context"
	"strings"
	"testing"
)

// notIdentified is the hazard a check names when it does not know which
// config.yaml the proxy runs on.
const notIdentified = "config.yaml not identified (set WT_LITELLM_CONFIG when the registry is redirected)"

// TestSubscriptionHazardsConfigNotIdentified pins the fail-closed reading of
// an empty config path (PairedConfigPath's answer when the registry is
// redirected and WT_LITELLM_CONFIG is unset): serve cannot check the proxy's
// settings, so that is itself a hazard (the mode stays off), never a silent
// "nothing to check"; the hazard carries no value.
func TestSubscriptionHazardsConfigNotIdentified(t *testing.T) {
	none := func(context.Context) ([]DeploymentInfo, error) { return nil, nil }
	hazards, _, err := SubscriptionHazards(t.Context(), "", ProxyEnvOf("test env", nil), none)
	if err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 1 || hazards[0] != notIdentified {
		t.Errorf("hazards = %q, want exactly %q", hazards, notIdentified)
	}
}

// TestSubscriptionGateConfigNotIdentified pins the sync gate's twin: a write
// that has no config.yaml to check (f nil) is blocked, so every subscription
// row is unrouted with the sync's own warning naming the reason, never held
// or routed.
func TestSubscriptionGateConfigNotIdentified(t *testing.T) {
	cfg := subscriptionConfig()
	g := gateSubscription(cfg, nil, cleanProxy(Options{}), nil)
	if g == nil || g.blocked != notIdentified {
		t.Fatalf("gate = %+v, want blocked %q", g, notIdentified)
	}
	off, hold, ws := subscriptionUnrouted(cfg, g)
	if !off[subID] || hold[subID] {
		t.Errorf("off %v hold %v, want %s unrouted", off, hold, subID)
	}
	if sw := subscriptionWarnings(ws); len(sw) != 1 || !strings.Contains(sw[0], "are not routed: "+notIdentified) {
		t.Errorf("warnings = %q, want one 'not routed' naming the reason", sw)
	}
}
