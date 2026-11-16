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

// TestToModelHostContextOnlyForCloud pins that the server's context window
// is a cloud model's host context and never a local model's.
func TestToModelHostContextOnlyForCloud(t *testing.T) {
	if got := ToModel(modelapi.Row{Location: "cloud", Context: 1000}).HostContext; got != 1000 {
		t.Errorf("cloud HostContext = %d", got)
	}
	if got := ToModel(modelapi.Row{Location: "local", Context: 1000}).HostContext; got != 0 {
		t.Errorf("local HostContext = %d", got)
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
	if FromWire(modelapi.Row{State: "unknown"}).Status != StatusUnknown {
		t.Error("unknown state")
	}
	b := FromWire(modelapi.Row{State: "blocked", Action: "block", BlockReason: "why", Location: "local"})
	if b.Action() != ActionBlock || b.BlockReason() != "why" {
		t.Errorf("block row %v %q", b.Action(), b.BlockReason())
	}
}
