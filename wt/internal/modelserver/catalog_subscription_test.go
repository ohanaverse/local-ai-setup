package modelserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// subCfg is testCfg plus the claude-subscription provider, one subscription
// model, one OpenRouter model and a group whose order 1 is the subscription
// model and order 2 the OpenRouter one.
func subCfg() *config.Config {
	cfg := testCfg(
		config.Model{ID: "claude-haiku-5-5", Family: "claude", ProviderID: config.SubscriptionProvider, ModelName: "claude-haiku-5-5", ContextWindow: 1000000},
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"},
	)
	cfg.Providers = append(cfg.Providers, config.Provider{ID: config.SubscriptionProvider, Location: config.LocationCloud})
	cfg.Aliases = []config.Alias{{ID: "alias/haiku", Targets: []config.AliasTarget{
		{Model: "claude-haiku-5-5", Order: 1}, {Model: "openrouter/c", Order: 2},
	}}}
	return cfg
}

// TestCatalogSubscriptionRowBlockedWhenOff pins decision 6: with the host's
// passthrough off, a subscription row is blocked and its reason names the
// key that turns it on ([gateway] subscription_passthrough), so a session
// refuses at resolve with the fix in hand instead of sending a login the
// gateway would refuse.
func TestCatalogSubscriptionRowBlockedWhenOff(t *testing.T) {
	r := rowByID(t, BuildCatalog(subCfg(), localmodels.Snapshot{}, nil, testSessions(), nil, testNow, CatalogOptions{}), "claude-haiku-5-5")
	if r.State != modelapi.StateBlocked || r.Action != modelapi.ActionBlock {
		t.Errorf("state/action = %s/%s, want blocked/block", r.State, r.Action)
	}
	if !strings.Contains(r.BlockReason, "[gateway] subscription_passthrough") {
		t.Errorf("block reason %q does not name [gateway] subscription_passthrough", r.BlockReason)
	}
	// Other rows are untouched by the mode.
	if o := rowByID(t, BuildCatalog(subCfg(), localmodels.Snapshot{}, nil, testSessions(), nil, testNow, CatalogOptions{}), "openrouter/c"); o.Action != modelapi.ActionLaunch {
		t.Errorf("openrouter/c action = %s, want launch", o.Action)
	}
}

// TestCatalogSubscriptionRowLaunchableWhenOn pins the other half: with the
// passthrough on, a subscription row is an ordinary cloud row, launchable
// with no block reason and its registry window.
func TestCatalogSubscriptionRowLaunchableWhenOn(t *testing.T) {
	r := rowByID(t, BuildCatalog(subCfg(), localmodels.Snapshot{}, nil, testSessions(), nil, testNow, CatalogOptions{SubscriptionOn: true}), "claude-haiku-5-5")
	if r.Action != modelapi.ActionLaunch || r.State == modelapi.StateBlocked || r.BlockReason != "" {
		t.Errorf("state/action/reason = %s/%s/%q, want launchable", r.State, r.Action, r.BlockReason)
	}
	if r.ProviderID != config.SubscriptionProvider || r.Context != 1000000 {
		t.Errorf("provider/context = %s/%d", r.ProviderID, r.Context)
	}
}

// TestSubscriptionFirstGroupDegradesWhenOff pins that a group whose order 1
// is a subscription model takes its order-2 target's action while the mode
// is off (aliasAction degrades over a blocked target, as phase 2 does): the
// order-2 target is a stopped local model, so the group is start while off
// and launch (through the subscription target) when on. The row stays an
// alias of both targets either way: only the action changes.
func TestSubscriptionFirstGroupDegradesWhenOff(t *testing.T) {
	cfg := subCfg()
	cfg.Models = append(cfg.Models, config.Model{ID: "omlx/a", Family: "f", ProviderID: "omlx", ModelName: "a"})
	cfg.Aliases[0].Targets[1].Model = "omlx/a"
	snap := localmodels.Snapshot{
		Entries:   []localmodels.Entry{omlxEntry("omlx/a", "a", false)},
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
	}
	for _, c := range []struct {
		on     bool
		action string
	}{{false, modelapi.ActionStart}, {true, modelapi.ActionLaunch}} {
		cat := BuildCatalog(cfg, snap, nil, testSessions(), nil, testNow, CatalogOptions{SubscriptionOn: c.on})
		a, local := rowByID(t, cat, "alias/haiku"), rowByID(t, cat, "omlx/a")
		if local.Action != modelapi.ActionStart {
			t.Fatalf("fixture: omlx/a action = %s, want start", local.Action)
		}
		if a.Action != c.action || a.BlockReason != "" {
			t.Errorf("on=%t: alias action/reason = %s/%q, want %s", c.on, a.Action, a.BlockReason, c.action)
		}
		if len(a.Targets) != 2 || a.Targets[0].ProviderID != config.SubscriptionProvider {
			t.Errorf("on=%t: targets = %+v", c.on, a.Targets)
		}
	}
	// With only the subscription target, the group blocks while off and names
	// the key through its target's reason.
	cfg = subCfg()
	cfg.Aliases[0].Targets = cfg.Aliases[0].Targets[:1]
	a := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), nil, testNow, CatalogOptions{}), "alias/haiku")
	if a.Action != modelapi.ActionBlock || !strings.Contains(a.BlockReason, "subscription_passthrough") {
		t.Errorf("subscription-only alias while off: action/reason = %s/%q", a.Action, a.BlockReason)
	}
}

// TestServerCatalogFollowsSubscriptionPassthrough pins that the server hands
// its own Options.SubscriptionPassthrough to BuildCatalog on both read paths:
// with it off, GET resolve and GET catalog give a subscription row block with
// the reason naming [gateway] subscription_passthrough; with it on, launch. A
// server that passed a constant would offer the row while the gateway refuses
// it, or block it while the gateway accepts it.
func TestServerCatalogFollowsSubscriptionPassthrough(t *testing.T) {
	cfg := serverCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: config.SubscriptionProvider, Location: config.LocationCloud})
	cfg.Models = append(cfg.Models, config.Model{ID: "claude-haiku-5-5", Family: "claude", ProviderID: config.SubscriptionProvider, ModelName: "claude-haiku-5-5"})
	for _, on := range []bool{false, true} {
		s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, SubscriptionPassthrough: on},
			(&fakeLife{snap: serverSnap()}).deps(cfg))
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		check := func(path string, r modelapi.Row) {
			t.Helper()
			if on {
				if r.Action != modelapi.ActionLaunch || r.BlockReason != "" {
					t.Errorf("on, %s: action/reason = %s/%q, want launch", path, r.Action, r.BlockReason)
				}
				return
			}
			if r.Action != modelapi.ActionBlock || !strings.Contains(r.BlockReason, "[gateway] subscription_passthrough") {
				t.Errorf("off, %s: action/reason = %s/%q, want block naming the key", path, r.Action, r.BlockReason)
			}
		}
		st, b := call(t, ts, "GET", "/control/v1/resolve?model=claude-haiku-5-5", "")
		if st != http.StatusOK {
			t.Fatalf("on=%t: resolve = %d %s", on, st, b)
		}
		var row modelapi.Row
		if err := json.Unmarshal(b, &row); err != nil {
			t.Fatal(err)
		}
		check("resolve", row)
		st, b = call(t, ts, "GET", "/control/v1/catalog", "")
		if st != http.StatusOK {
			t.Fatalf("on=%t: catalog = %d %s", on, st, b)
		}
		var cat modelapi.Catalog
		if err := json.Unmarshal(b, &cat); err != nil {
			t.Fatal(err)
		}
		check("catalog", rowByID(t, cat, "claude-haiku-5-5"))
	}
}
