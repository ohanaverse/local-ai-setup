package modelserver

import (
	"fmt"
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
// routed, and everything else blocks with a reason. The state follows the
// action, as on every model row: launch is running, block is blocked, start
// is stopped (or the local target's own state when the start is its start).
// A wrong answer makes a client launch an alias the gateway cannot route,
// never start its model, or read a row whose state and action disagree.
func TestAliasActionTable(t *testing.T) {
	cloudUp := modelapi.Row{ID: "openrouter/c", Location: "cloud", State: modelapi.StateStopped, Action: modelapi.ActionLaunch}
	localStopped := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart}
	localRunning := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch}
	cloudBlocked := modelapi.Row{ID: "openrouter/c", Location: "cloud", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "no key"}
	localBlocked := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "omlx is down"}
	localLoading := modelapi.Row{ID: "omlx/a", Location: "local", State: modelapi.StateLoading, Action: modelapi.ActionStart}
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
			"local", modelapi.StateStopped, modelapi.ActionStart, ""},
		{"local loading at order 1", []modelapi.AliasTarget{tg("omlx/a", 1)}, rowsOf(localLoading), true,
			"local", modelapi.StateLoading, modelapi.ActionStart, ""},
		{"local blocked at order 1, cloud fallback routed", []modelapi.AliasTarget{tg("omlx/a", 1), tg("openrouter/c", 2)}, rowsOf(localBlocked, cloudUp), true,
			"local", modelapi.StateRunning, modelapi.ActionLaunch, ""},
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

// TestAliasRowsKeepTheWireInvariants runs aliasRows over every combination
// of a local order-1 target's and a cloud order-2 target's states, routed or
// not, and checks each row against the rules every catalog row obeys (and
// TestFixtureRowsAgreeWithServerRules pins on the fixture): blocked exactly
// when the action is block, a block_reason exactly when blocked, a running
// row launches, and a start row is one catalog.NeedsStart starts. Session
// readers (wt model list --server, the launch, smoke) rely on them.
func TestAliasRowsKeepTheWireInvariants(t *testing.T) {
	cfg := aliasCfg()
	cfg.Aliases = []config.Alias{{ID: "alias/x", Targets: []config.AliasTarget{{Model: "omlx/a", Order: 1}, {Model: "openrouter/c", Order: 2}}}}
	if fault := config.AliasFault(cfg, cfg.Aliases[0]); fault != nil {
		t.Fatalf("fixture alias is faulty: %v", fault)
	}
	locals := []modelapi.Row{
		{ID: "omlx/a", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "omlx/a", Location: "local", State: modelapi.StateLoading, Action: modelapi.ActionStart},
		{ID: "omlx/a", Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart},
		{ID: "omlx/a", Location: "local", State: modelapi.StateUnknown, Action: modelapi.ActionStart},
		{ID: "omlx/a", Location: "local", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "omlx is down"},
	}
	clouds := []modelapi.Row{
		{ID: "openrouter/c", Location: "cloud", State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "openrouter/c", Location: "cloud", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "no key"},
	}
	for _, l := range locals {
		for _, c := range clouds {
			for _, routed := range []map[string]bool{nil, {"alias/x": true}, {}} {
				rows := aliasRows(cfg, []modelapi.Row{l, c}, nil, testSessions(), routed, 0)
				if len(rows) != 1 {
					t.Fatalf("rows = %d", len(rows))
				}
				r := rows[0]
				if r.Location != "local" {
					t.Fatalf("location %s, want local (the order-1 target is local)", r.Location)
				}
				name := fmt.Sprintf("%s/%s/routed=%v", l.State, c.State, routed)
				if (r.State == modelapi.StateBlocked) != (r.Action == modelapi.ActionBlock) {
					t.Errorf("%s: state %s and action %s disagree about blocking", name, r.State, r.Action)
				}
				if (r.BlockReason != "") != (r.State == modelapi.StateBlocked) {
					t.Errorf("%s: block_reason %q with state %s", name, r.BlockReason, r.State)
				}
				if r.State == modelapi.StateRunning && r.Action != modelapi.ActionLaunch {
					t.Errorf("%s: a running row has action %s", name, r.Action)
				}
				if r.Action == modelapi.ActionStart && !catalog.NeedsStart(r) {
					t.Errorf("%s: a start row catalog.NeedsStart does not start", name)
				}
			}
		}
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
	c := BuildCatalog(cfg, aliasSnap(false), ix, sess, map[string]bool{"alias/x": true, "openrouter/c": true}, testNow, CatalogOptions{})
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
// give the alias the lower of their tiers (targets with equal order share
// the requests, measured on LiteLLM 1.109.1), so a picker never advertises an alias above what some of
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
	r := rowByID(t, BuildCatalog(cfg, aliasSnap(true), ix, testSessions(), nil, testNow, CatalogOptions{}), "alias/x")
	if r.Tier != "quick" || r.TierCode != "Q" || r.Confidence != "low" {
		t.Errorf("tier = %q %q %q, want quick", r.Tier, r.TierCode, r.Confidence)
	}
}

// TestAliasRowContextIsTheMinimum pins Review Focus 4: the alias declares the
// smallest window over all its targets, so claude never compacts too late on
// the target with the smaller window.
func TestAliasRowContextIsTheMinimum(t *testing.T) {
	r := rowByID(t, BuildCatalog(aliasCfg(), aliasSnap(true), nil, testSessions(), nil, testNow, CatalogOptions{}), "alias/x")
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
	r := rowByID(t, BuildCatalog(cfg, aliasSnap(true), nil, testSessions(), nil, testNow, CatalogOptions{}), "alias/x")
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
	c := BuildCatalog(cfg, snap, ix, testSessions(), nil, testNow, CatalogOptions{})
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
	c := BuildCatalog(cfg, aliasSnap(true), nil, testSessions(), nil, testNow, CatalogOptions{})
	for id, want := range map[string]string{"alias/bad": "targets is required", "alias/orphan": "names no model"} {
		r := rowByID(t, c, id)
		if r.Action != modelapi.ActionBlock || r.State != modelapi.StateBlocked || !strings.Contains(r.BlockReason, want) {
			t.Errorf("%s: %s/%s %q, want a block naming %q", id, r.State, r.Action, r.BlockReason, want)
		}
	}
}

// TestRedirectAliasRowStaysLaunchableAndNamesItsTarget pins #687 on the wire:
// a redirect alias is a launchable alias row with its target alias's targets
// and window, carrying redirect_to so a client can note the rename, and a
// session on the old id still counts against the local target (decision 12),
// so the target is not stopped under it. A redirect to a missing alias is a
// block row naming the fault. Without this a running session on the old id
// would lose its row at the rename.
func TestRedirectAliasRowStaysLaunchableAndNamesItsTarget(t *testing.T) {
	cfg := aliasCfg()
	cfg.Aliases = append(cfg.Aliases, config.Alias{ID: "alias/old", RedirectTo: "alias/x"})
	sess := testSessions()
	_ = sess.Register("s", []string{"alias/old"})
	routed := map[string]bool{"alias/x": true, "alias/old": true, "openrouter/c": true}
	c := BuildCatalog(cfg, aliasSnap(false), nil, sess, routed, testNow, CatalogOptions{})
	nw, old := rowByID(t, c, "alias/x"), rowByID(t, c, "alias/old")
	if old.RedirectTo != "alias/x" || nw.RedirectTo != "" || old.Kind != modelapi.KindAlias {
		t.Fatalf("redirect_to: old %q new %q", old.RedirectTo, nw.RedirectTo)
	}
	if old.Action != nw.Action || old.State != nw.State || len(old.Targets) != len(nw.Targets) || len(old.Targets) == 0 || old.Context != nw.Context || old.Sessions != 1 {
		t.Errorf("old row %+v differs from the new row %+v beyond the id", old, nw)
	}
	if got := aliasExpansion(cfg)["alias/old"]; len(got) != 1 || got[0] != "omlx/a" {
		t.Errorf("a session on the redirect expands to %v, want the local target omlx/a", got)
	}
	cfg.Aliases[len(cfg.Aliases)-1].RedirectTo = "alias/ghost"
	c = BuildCatalog(cfg, aliasSnap(false), nil, sess, routed, testNow, CatalogOptions{})
	if r := rowByID(t, c, "alias/old"); r.Action != modelapi.ActionBlock || !strings.Contains(r.BlockReason, "not an alias") {
		t.Errorf("redirect to a missing alias: %+v", r)
	}
}

// TestAliasActionWithSeveralLocalTargets pins #690's policy for an alias whose
// lowest-order level holds local targets: the action reads every local target
// (start when any must start, or any runs while the alias is not routed), the
// state is that target's when exactly one must start, loading when any is
// loading, else stopped; a cloud-first alias keeps launching without starting
// its local fallbacks.
func TestAliasActionWithSeveralLocalTargets(t *testing.T) {
	local := func(id, state, action string) modelapi.Row {
		return modelapi.Row{ID: id, Location: "local", State: state, Action: action}
	}
	stopped := func(id string) modelapi.Row { return local(id, modelapi.StateStopped, modelapi.ActionStart) }
	loading := func(id string) modelapi.Row { return local(id, modelapi.StateLoading, modelapi.ActionStart) }
	running := func(id string) modelapi.Row { return local(id, modelapi.StateRunning, modelapi.ActionLaunch) }
	cloudUp := modelapi.Row{ID: "openrouter/c", Location: "cloud", State: modelapi.StateStopped, Action: modelapi.ActionLaunch}
	tg := func(id string, order int) modelapi.AliasTarget { return modelapi.AliasTarget{ID: id, Order: order} }
	rowsOf := func(rs ...modelapi.Row) map[string]modelapi.Row {
		m := map[string]modelapi.Row{}
		for _, r := range rs {
			m[r.ID] = r
		}
		return m
	}
	two := []modelapi.AliasTarget{tg("omlx/a", 1), tg("omlx/b", 2)}
	for _, c := range []struct {
		name         string
		targets      []modelapi.AliasTarget
		rows         map[string]modelapi.Row
		routed       bool
		loc, st, act string
	}{
		{"both stopped", two, rowsOf(stopped("omlx/a"), stopped("omlx/b")), true, "local", modelapi.StateStopped, modelapi.ActionStart},
		{"one running, one stopped", two, rowsOf(running("omlx/a"), stopped("omlx/b")), true, "local", modelapi.StateStopped, modelapi.ActionStart},
		{"one running, one loading", two, rowsOf(running("omlx/a"), loading("omlx/b")), true, "local", modelapi.StateLoading, modelapi.ActionStart},
		{"both stopped, one loading", two, rowsOf(stopped("omlx/a"), loading("omlx/b")), true, "local", modelapi.StateLoading, modelapi.ActionStart},
		{"both running, routed", two, rowsOf(running("omlx/a"), running("omlx/b")), true, "local", modelapi.StateRunning, modelapi.ActionLaunch},
		{"both running, alias unrouted", two, rowsOf(running("omlx/a"), running("omlx/b")), false, "local", modelapi.StateStopped, modelapi.ActionStart},
		{"cloud first, two local fallbacks stopped", []modelapi.AliasTarget{tg("openrouter/c", 1), tg("omlx/a", 2), tg("omlx/b", 3)},
			rowsOf(cloudUp, stopped("omlx/a"), stopped("omlx/b")), true, "cloud", modelapi.StateRunning, modelapi.ActionLaunch},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, state, action, reason := aliasAction(c.targets, c.rows, c.routed)
			if loc != c.loc || state != c.st || action != c.act || reason != "" {
				t.Errorf("got %s/%s/%s (%q), want %s/%s/%s", loc, state, action, reason, c.loc, c.st, c.act)
			}
		})
	}
}
