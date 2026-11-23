package modelserver

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestAliasActionTable pins decision 5's policy for an alias row, one case
// per rule: the location follows the lowest-order level, a local target at
// that level that must start (or runs while the alias is not routed) makes
// the alias start, a servable target launches only while the alias is
// routed, and everything else blocks with a reason (a block action is always
// the blocked state, as on every model row). A wrong answer makes a
// client launch an alias the gateway cannot route or never start its model.
func TestAliasActionTable(t *testing.T) {
	cloudUp := modelapi.Row{ID: "openrouter/c", Location: "cloud", State: modelapi.StateStopped, Action: modelapi.ActionLaunch}
	localStopped := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart}
	localRunning := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch}
	cloudBlocked := modelapi.Row{ID: "openrouter/c", Location: "cloud", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "no key"}
	localBlocked := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "omlx is down"}
	tg := func(id string, order int) modelapi.AliasTarget { return modelapi.AliasTarget{ID: id, Order: order} }
	rowsOf := func(rs ...modelapi.Row) map[string]modelapi.Row {
		m := map[string]modelapi.Row{}
		for _, r := range rs {
			m[r.ID] = r
		}
		return m
	}
	for _, c := range []struct {
		name                     string
		targets                  []modelapi.AliasTarget
		rows                     map[string]modelapi.Row
		routed                   bool
		loc, state, action, want string // want: a substring of the block reason
	}{
		{"cloud up, local stopped at order 2", []modelapi.AliasTarget{tg("openrouter/c", 1), tg("omlx/a", 2)}, rowsOf(cloudUp, localStopped), true,
			"cloud", modelapi.StateRunning, modelapi.ActionLaunch, ""},
		{"local stopped at order 1", []modelapi.AliasTarget{tg("omlx/a", 1), tg("openrouter/c", 2)}, rowsOf(cloudUp, localStopped), true,
			"local", modelapi.StateStopped, modelapi.ActionStart, ""},
		{"local running at order 1, alias unrouted", []modelapi.AliasTarget{tg("omlx/a", 1)}, rowsOf(localRunning), false,
			"local", modelapi.StateRunning, modelapi.ActionStart, ""},
		{"local running at order 1, alias routed", []modelapi.AliasTarget{tg("omlx/a", 1)}, rowsOf(localRunning), true,
			"local", modelapi.StateRunning, modelapi.ActionLaunch, ""},
		{"cloud servable, alias unrouted", []modelapi.AliasTarget{tg("openrouter/c", 1)}, rowsOf(cloudUp), false,
			"cloud", modelapi.StateBlocked, modelapi.ActionBlock, "'wt-model litellm sync'"},
		{"cloud blocked, local stopped at order 2", []modelapi.AliasTarget{tg("openrouter/c", 1), tg("omlx/a", 2)}, rowsOf(cloudBlocked, localStopped), true,
			"cloud", modelapi.StateStopped, modelapi.ActionStart, ""},
		{"all blocked", []modelapi.AliasTarget{tg("openrouter/c", 1), tg("omlx/a", 2)}, rowsOf(cloudBlocked, localBlocked), true,
			"cloud", modelapi.StateBlocked, modelapi.ActionBlock, "openrouter/c: no key; omlx/a: omlx is down"},
		{"missing target", []modelapi.AliasTarget{tg("openrouter/c", 1), tg("omlx/gone", 2)}, rowsOf(cloudUp), true,
			"cloud", modelapi.StateBlocked, modelapi.ActionBlock, "omlx/gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, state, action, reason := aliasAction(c.targets, c.rows, c.routed)
			if loc != c.loc || state != c.state || action != c.action {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", loc, state, action, c.loc, c.state, c.action)
			}
			if (action == modelapi.ActionBlock) != (reason != "") {
				t.Errorf("action %s with reason %q", action, reason)
			}
			if c.want != "" && !strings.Contains(reason, c.want) {
				t.Errorf("reason %q, want it to contain %q", reason, c.want)
			}
			if c.name == "local running at order 1, alias unrouted" {
				routed := c.routed
				row := modelapi.Row{Location: loc, State: state, Action: action, Routed: &routed}
				if !catalog.NeedsStart(row) {
					t.Error("catalog.NeedsStart disagrees with the start action")
				}
			}
		})
	}
}

// aliasCfg is a registry with a cloud model, a local model and one alias
// over both (cloud first).
func aliasCfg() *config.Config {
	cfg := testCfg(
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c", ContextWindow: 200000},
		config.Model{ID: "omlx/a", Family: "f", ProviderID: "omlx", ModelName: "a", ContextWindow: 131072},
	)
	cfg.Aliases = []config.Alias{{ID: "alias/x", Targets: []config.AliasTarget{{Model: "openrouter/c", Order: 1}, {Model: "omlx/a", Order: 2}}}}
	return cfg
}

func aliasSnap(running bool) localmodels.Snapshot {
	return localmodels.Snapshot{
		Entries:   []localmodels.Entry{omlxEntry("omlx/a", "a", running)},
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
	}
}

// TestAliasRowShape pins the alias row decision 5 describes: id and gateway
// id are the alias id, provider and family "alias", the name after "alias/"
// as model_name, registered, ordered after every registry model, Kind alias,
// its targets in order with their own provider, location, state and window,
// the tier of the lowest-order target, no price, and the alias's live client
// sessions. A client launches and labels the alias from exactly these.
func TestAliasRowShape(t *testing.T) {
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "c"
tier = "standard"
code = "S"
confidence = "high"
`))
	if err != nil {
		t.Fatal(err)
	}
	sess := testSessions()
	cfg := aliasCfg()
	tapConfig(Deps{Config: func() *config.Config { return cfg }}, sess).Config() // the server's wiring
	_ = sess.Register("s", []string{"alias/x"})
	c := BuildCatalog(cfg, aliasSnap(false), ix, sess, map[string]bool{"alias/x": true, "openrouter/c": true}, testNow)
	r := rowByID(t, c, "alias/x")
	if r.GatewayID != "alias/x" || r.ProviderID != "alias" || r.Family != "alias" || r.ModelName != "x" || !r.Registered || r.Discovered {
		t.Errorf("identity = %+v", r)
	}
	if r.Kind != modelapi.KindAlias || r.Order != 2 || r.Price != nil || r.Sessions != 1 || r.Routed == nil || !*r.Routed {
		t.Errorf("kind/order/price/sessions/routed = %q %d %v %d %v", r.Kind, r.Order, r.Price, r.Sessions, r.Routed)
	}
	if r.Tier != "standard" || r.TierCode != "S" {
		t.Errorf("tier = %q %q, want the order-1 target's", r.Tier, r.TierCode)
	}
	want := []modelapi.AliasTarget{
		{ID: "openrouter/c", Order: 1, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateStopped, Context: 200000},
		{ID: "omlx/a", Order: 2, ProviderID: "omlx", Location: "local", State: modelapi.StateStopped, Context: 131072},
	}
	if !reflect.DeepEqual(r.Targets, want) {
		t.Errorf("targets = %+v", r.Targets)
	}
	if r.Location != "cloud" || r.State != modelapi.StateRunning || r.Action != modelapi.ActionLaunch {
		t.Errorf("location/state/action = %s/%s/%s", r.Location, r.State, r.Action)
	}
	if rowByID(t, c, "omlx/a").Sessions != 1 {
		t.Error("the local target's row does not count the alias's client session")
	}
}

// TestAliasRowTierIsTheLowestAmongEqualOrders pins that equal-order targets
// give the alias the lower of their tiers (targets with equal order are
// written as is; whether LiteLLM spreads requests across them is checked in
// the live check), so a picker never advertises an alias above what some of
// its requests may get.
func TestAliasRowTierIsTheLowestAmongEqualOrders(t *testing.T) {
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "c"
tier = "deep"
code = "D"
confidence = "high"

[[models]]
provider_id = "omlx"
model_name = "a"
tier = "quick"
code = "Q"
confidence = "low"
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := aliasCfg()
	cfg.Aliases[0].Targets[1].Order = 1
	r := rowByID(t, BuildCatalog(cfg, aliasSnap(true), ix, testSessions(), nil, testNow), "alias/x")
	if r.Tier != "quick" || r.TierCode != "Q" || r.Confidence != "low" {
		t.Errorf("tier = %q %q %q, want quick", r.Tier, r.TierCode, r.Confidence)
	}
}

// TestAliasRowContextIsTheMinimum pins Review Focus 4: the alias declares the
// smallest window over all its targets, so claude never compacts too late on
// the target with the smaller window.
func TestAliasRowContextIsTheMinimum(t *testing.T) {
	r := rowByID(t, BuildCatalog(aliasCfg(), aliasSnap(true), nil, testSessions(), nil, testNow), "alias/x")
	if r.Context != 131072 {
		t.Errorf("context = %d, want the minimum 131072", r.Context)
	}
}

// TestAliasRowContextWithheldWithAnUnknownTarget pins that one target with
// an unknown window withholds the alias's window (0): a minimum over the
// known targets alone would over-declare.
func TestAliasRowContextWithheldWithAnUnknownTarget(t *testing.T) {
	cfg := aliasCfg()
	cfg.Models[1].ContextWindow = 0
	r := rowByID(t, BuildCatalog(cfg, aliasSnap(true), nil, testSessions(), nil, testNow), "alias/x")
	if r.Context != 0 {
		t.Errorf("context = %d, want 0 (withheld)", r.Context)
	}
	if r.Targets[1].Context != 0 || r.Targets[0].Context != 200000 {
		t.Errorf("target contexts = %+v", r.Targets)
	}
}

// TestLocalRowCarriesRegistryWindow pins decision 6 on the model rows: a
// local row carries its registry context_window, and a local row never takes
// a tiers window (a local server serves whatever it was started with), while
// a cloud row still takes its host tiers window.
func TestLocalRowCarriesRegistryWindow(t *testing.T) {
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "omlx"
model_name = "b"
context = 32768
context_basis = "host"

[[models]]
provider_id = "openrouter"
model_name = "c"
context = 100000
context_basis = "host"
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCfg(
		config.Model{ID: "omlx/a", Family: "f", ProviderID: "omlx", ModelName: "a", ContextWindow: 65536},
		config.Model{ID: "omlx/b", Family: "f", ProviderID: "omlx", ModelName: "b"},
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"},
	)
	snap := aliasSnap(true)
	snap.Entries = append(snap.Entries, omlxEntry("omlx/b", "b", true))
	c := BuildCatalog(cfg, snap, ix, testSessions(), nil, testNow)
	for id, want := range map[string]int{"omlx/a": 65536, "omlx/b": 0, "openrouter/c": 100000} {
		if got := rowByID(t, c, id).Context; got != want {
			t.Errorf("%s: context = %d, want %d", id, got, want)
		}
	}
}

// TestMalformedAliasIsABlockRow pins that an alias the registry reader marked
// malformed, or one config.AliasFault rejects, is still listed, as a block
// row naming its fault: a slot profile naming it then says why instead of
// "unknown model".
func TestMalformedAliasIsABlockRow(t *testing.T) {
	cfg := aliasCfg()
	cfg.Aliases = append(cfg.Aliases,
		config.Alias{ID: "alias/bad", Malformed: "targets is required"},
		config.Alias{ID: "alias/orphan", Targets: []config.AliasTarget{{Model: "omlx/gone", Order: 1}}})
	c := BuildCatalog(cfg, aliasSnap(true), nil, testSessions(), nil, testNow)
	for id, want := range map[string]string{"alias/bad": "targets is required", "alias/orphan": "names no model"} {
		r := rowByID(t, c, id)
		if r.Action != modelapi.ActionBlock || r.State != modelapi.StateBlocked || !strings.Contains(r.BlockReason, want) {
			t.Errorf("%s: %s/%s %q, want a block naming %q", id, r.State, r.Action, r.BlockReason, want)
		}
	}
}
