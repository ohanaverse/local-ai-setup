package catalog

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

func fp(v float64) *float64 { return &v }
func bp(v bool) *bool       { return &v }

func wid(rows []modelapi.Row) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestToModelNativeKeepsNoGatewayFacts pins that a native wire row becomes a
// Native model with an empty GatewayID, so nothing routes it by a gateway id.
func TestToModelNativeKeepsNoGatewayFacts(t *testing.T) {
	m := ToModel(modelapi.Row{ID: "claude/native", Native: true, ProviderID: "claude", Location: "cloud"})
	if !m.Native || m.GatewayID != "" || m.ID != "claude/native" || m.Location != config.LocationCloud {
		t.Errorf("model = %+v", m)
	}
}

// TestToModelCarriesGatewayID pins that the id an agent must send, the
// server's gateway_id, survives conversion, with price and identity fields.
func TestToModelCarriesGatewayID(t *testing.T) {
	m := ToModel(modelapi.Row{ID: "or/x", GatewayID: "gw-x", ProviderID: "or", ModelName: "x", Family: "f",
		Tags: []string{"a"}, Location: "cloud", Price: &modelapi.Price{Input: fp(1), Cache: fp(2), Output: fp(3)}})
	if m.GatewayID != "gw-x" || m.ProviderID != "or" || m.ModelName != "x" || m.Family != "f" || !slices.Equal(m.Tags, []string{"a"}) {
		t.Errorf("model = %+v", m)
	}
	c := m.Cost
	if c.InputPricePerMillion == nil || *c.InputPricePerMillion != 1 || *c.CachePricePerMillion != 2 || *c.OutputPricePerMillion != 3 {
		t.Errorf("cost = %+v", c)
	}
}

// TestToModelKeepsALocalRowsContext pins that the server's context window
// becomes HostContext for every row, local and alias rows included: the
// server now sends a local row's window only from the registry's
// context_window (#626), and an alias's is the minimum over its targets, so
// a session that dropped it would leave claude on its assumed 200K window.
func TestToModelKeepsALocalRowsContext(t *testing.T) {
	for _, r := range []modelapi.Row{
		{Location: "cloud", Context: 1000},
		{Location: "local", Context: 1000},
		{Location: "local", Kind: modelapi.KindAlias, ProviderID: "alias", Context: 1000},
	} {
		if got := ToModel(r).HostContext; got != 1000 {
			t.Errorf("%s %q row: HostContext = %d, want 1000", r.Location, r.Kind, got)
		}
	}
	if got := ToModel(modelapi.Row{Location: "local"}).HostContext; got != 0 {
		t.Errorf("a row with no context: HostContext = %d, want 0", got)
	}
}

func eligRows() []modelapi.Row {
	return []modelapi.Row{
		{ID: "c", ProviderID: "claude", Native: true, Order: 3, Family: "f1", Tags: []string{"x"}},
		{ID: "b", ProviderID: "ollama", Order: 1, Family: "f2", Tags: []string{"y"}},
		{ID: "a", ProviderID: "sushi", Order: 0, Family: "f1", Tags: []string{"x", "y"}},
		{ID: "d", ProviderID: "copilot", Native: true, Order: 2, Family: "f1"},
	}
}

// TestEligibleWireByProviderTagFamily pins provider, tag and family filters
// as Config.EligibleModelsIn applies them.
func TestEligibleWireByProviderTagFamily(t *testing.T) {
	a := config.Agent{Name: "codex", SupportedProviders: []string{"ollama", "copilot"}}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"b", "d"}) {
		t.Errorf("providers: %v", got)
	}
	if got := wid(EligibleWire(eligRows(), a, "y", "")); !slices.Equal(got, []string{"b"}) {
		t.Errorf("tags: %v", got)
	}
	if got := wid(EligibleWire(eligRows(), a, "", "f1")); !slices.Equal(got, []string{"d"}) {
		t.Errorf("family: %v", got)
	}
}

// TestEligibleWireSushiForClaudeAndPi pins that sushi rows reach claude and
// pi through SushiAgents even when the agent lists no sushi provider.
func TestEligibleWireSushiForClaudeAndPi(t *testing.T) {
	for _, name := range []string{"claude", "pi", "codex"} {
		a := config.Agent{Name: name, SupportedProviders: []string{"none"}}
		got := wid(EligibleWire(eligRows(), a, "", ""))
		want := name != "codex"
		if slices.Contains(got, "a") != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// TestEligibleWireAllProvidersExcludesOthersNative pins that an AllProviders
// claude sees its own native row and every non-native row, not copilot's.
func TestEligibleWireAllProvidersExcludesOthersNative(t *testing.T) {
	a := config.Agent{Name: "claude", AllProviders: true}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}

// TestEligibleWireOrdersByOrder pins that the result follows Row.Order, not
// the input order.
func TestEligibleWireOrdersByOrder(t *testing.T) {
	a := config.Agent{Name: "x", SupportedProviders: []string{"claude", "ollama", "sushi", "copilot"}}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"a", "b", "d", "c"}) {
		t.Errorf("got %v", got)
	}
}

// aliasRow is an alias row over targets, as the model server sends it.
func aliasRow(id, loc string, order int, targets ...modelapi.AliasTarget) modelapi.Row {
	return modelapi.Row{ID: id, GatewayID: id, ProviderID: "alias", Family: "alias", Kind: modelapi.KindAlias,
		Location: loc, Order: order, Registered: true, Action: modelapi.ActionLaunch, State: modelapi.StateRunning, Targets: targets}
}

// TestEligibleWireAliasNeedsEveryTargetProvider pins that an alias row (whose
// own provider id is "alias", which no agent lists) reaches an agent exactly
// when every one of its targets' providers would: a group with one target
// the agent cannot use would fall back, through the gateway, onto a model
// the agent was never meant to run. An AllProviders agent takes every alias.
func TestEligibleWireAliasNeedsEveryTargetProvider(t *testing.T) {
	both := aliasRow("alias/both", "cloud", 5,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama", Location: "cloud"},
		modelapi.AliasTarget{ID: "omlx/y", Order: 2, ProviderID: "omlx", Location: "local"})
	ollamaOnly := aliasRow("alias/one", "cloud", 6,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama", Location: "cloud"})
	sushi := aliasRow("alias/sushi", "local", 7,
		modelapi.AliasTarget{ID: "sushi/p", Order: 1, ProviderID: "sushi", Location: "local"})
	rows := append(eligRows(), both, ollamaOnly, sushi)

	codex := config.Agent{Name: "codex", SupportedProviders: []string{"ollama"}}
	if got := wid(EligibleWire(rows, codex, "", "")); !slices.Equal(got, []string{"b", "alias/one"}) {
		t.Errorf("codex (ollama only): %v, want [b alias/one]", got)
	}
	wide := config.Agent{Name: "codex", SupportedProviders: []string{"ollama", "omlx"}}
	if got := wid(EligibleWire(rows, wide, "", "")); !slices.Equal(got, []string{"b", "alias/both", "alias/one"}) {
		t.Errorf("codex (ollama, omlx): %v, want both aliases", got)
	}
	pi := config.Agent{Name: "pi", SupportedProviders: []string{"none"}}
	if got := wid(EligibleWire(rows, pi, "", "")); !slices.Equal(got, []string{"a", "alias/sushi"}) {
		t.Errorf("pi (sushi through SushiAgents): %v, want [a alias/sushi]", got)
	}
	all := config.Agent{Name: "claude", AllProviders: true}
	if got := wid(EligibleWire(rows, all, "", "")); !slices.Equal(got, []string{"a", "b", "c", "alias/both", "alias/one", "alias/sushi"}) {
		t.Errorf("AllProviders: %v", got)
	}
	empty := aliasRow("alias/none", "cloud", 8)
	if got := wid(EligibleWire([]modelapi.Row{empty}, all, "", "")); len(got) != 0 {
		t.Errorf("an alias with no targets reached an agent: %v", got)
	}
}

// TestRotationSkipsAliasRows pins decision 14: alias rows are registered
// (they count in stats families) but RegisteredOrdered, the list a no-pin
// launch rotates over, leaves them out, so wt never rotates onto an alias
// the user did not ask for; the picker and -M alias/x still launch one.
func TestRotationSkipsAliasRows(t *testing.T) {
	rows := []modelapi.Row{
		{ID: "z", Order: 2, Registered: true, Action: "launch"},
		aliasRow("alias/x", "cloud", 3, modelapi.AliasTarget{ID: "z", Order: 1, ProviderID: "openrouter", Location: "cloud"}),
		{ID: "y", Order: 1, Registered: true, Action: "launch"},
	}
	var ids []string
	for _, m := range RegisteredOrdered(rows) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"y", "z"}) {
		t.Errorf("rotation list = %v, want [y z] (no alias)", ids)
	}
}

// TestNeedsStartRules pins which wire rows need a start before launch.
func TestNeedsStartRules(t *testing.T) {
	for _, c := range []struct {
		name string
		r    modelapi.Row
		want bool
	}{
		{"stopped local", modelapi.Row{Location: "local", State: "stopped", Action: "start"}, true},
		{"loading local", modelapi.Row{Location: "local", State: "loading", Action: "start"}, true},
		{"running local routed", modelapi.Row{Location: "local", State: "running", Action: "launch", Routed: bp(true)}, false},
		{"cloud", modelapi.Row{Location: "cloud", State: "stopped", Action: "launch"}, false},
		{"running cloud unrouted", modelapi.Row{Location: "cloud", State: "running", Action: "launch", Routed: bp(false)}, false},
	} {
		if got := NeedsStart(c.r); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// TestNeedsStartUnknownRouting pins R7: a running local row whose routing the
// server could not tell does not need a start; a known-unrouted one does.
func TestNeedsStartUnknownRouting(t *testing.T) {
	r := modelapi.Row{Location: "local", State: "running", Action: "launch"}
	if NeedsStart(r) {
		t.Error("unknown routing needs start")
	}
	r.Routed = bp(false)
	if !NeedsStart(r) {
		t.Error("unrouted running local does not need start")
	}
}

// TestLaunchableAndRegisteredOrdered pins Launchable (action launch) and
// RegisteredOrdered (registered rows only, in Order).
func TestLaunchableAndRegisteredOrdered(t *testing.T) {
	rows := []modelapi.Row{
		{ID: "z", Order: 2, Registered: true, Action: "launch"},
		{ID: "d", Order: 0, Discovered: true, Action: "launch"},
		{ID: "y", Order: 1, Registered: true, Action: "start"},
	}
	if got := wid(Launchable(rows)); !slices.Equal(got, []string{"z", "d"}) {
		t.Errorf("launchable %v", got)
	}
	var ids []string
	for _, m := range RegisteredOrdered(rows) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"y", "z"}) {
		t.Errorf("registered %v", ids)
	}
}

// TestFromWireRow pins the row fields FromWire derives from the wire state.
func TestFromWireRow(t *testing.T) {
	r := FromWire(modelapi.Row{ID: "m", Location: "local", State: "loading", Action: "start", Discovered: true})
	if !r.Running || !r.Loading || r.Status != StatusNew || !r.Discovered || r.Wire == nil || r.Ready() || r.Action() != ActionStart {
		t.Errorf("row = %+v", r)
	}
	u := FromWire(modelapi.Row{State: "unknown", Location: "local", Action: "start"})
	if u.Status != StatusUnknown || u.Action() != ActionStart {
		t.Errorf("unknown row = %v %v", u.Status, u.Action())
	}
	if run := FromWire(modelapi.Row{State: "running", Location: "local", Action: "launch"}); !run.Ready() || run.Action() != ActionLaunch {
		t.Errorf("running row ready=%v action=%v", run.Ready(), run.Action())
	}
	b := FromWire(modelapi.Row{State: "blocked", Action: "block", BlockReason: "why", Location: "local"})
	if b.Action() != ActionBlock || b.BlockReason() != "why" {
		t.Errorf("block row %v %q", b.Action(), b.BlockReason())
	}
}
