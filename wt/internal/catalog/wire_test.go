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

// TestEligibleWireByProviderTagFamily pins that provider, tag and family
// filters apply on top of the derived rule: codex, with no list, is offered
// every non-native row (and no other agent's native one), and the tag and
// family filters then narrow that.
func TestEligibleWireByProviderTagFamily(t *testing.T) {
	a := config.Agent{Name: "codex"}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("providers: %v", got)
	}
	if got := wid(EligibleWire(eligRows(), a, "y", "")); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("tag y: %v", got)
	}
	if got := wid(EligibleWire(eligRows(), a, "x", "")); !slices.Equal(got, []string{"a"}) {
		t.Errorf("tag x: %v", got)
	}
	if got := wid(EligibleWire(eligRows(), a, "", "f1")); !slices.Equal(got, []string{"a"}) {
		t.Errorf("family: %v", got)
	}
}

// TestEligibleWireSushiForEveryAgent pins that a sushi row reaches every agent
// that is not native-only (claude, pi, codex, opencode, copilot), and never agy, whose only offer is its own native
// row. Sushi used to be reserved to claude and pi by a table.
func TestEligibleWireSushiForEveryAgent(t *testing.T) {
	for _, name := range []string{"claude", "pi", "codex", "opencode", "copilot", "agy"} {
		a := config.Agent{Name: name}
		got := wid(EligibleWire(eligRows(), a, "", ""))
		want := name != "agy"
		if slices.Contains(got, "a") != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// TestEligibleWireSynthesizedExcludesOthersNative pins that a Synthesized
// claude sees its own native row and every non-native row, not copilot's.
func TestEligibleWireSynthesizedExcludesOthersNative(t *testing.T) {
	a := config.Agent{Name: "claude", Synthesized: true}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}

// TestEligibleWireOrdersByOrder pins that the result follows Row.Order, not
// the input order: claude gets sushi (0), ollama (1) and its own native row
// (3), and copilot's native row (2) is not offered to it.
func TestEligibleWireOrdersByOrder(t *testing.T) {
	a := config.Agent{Name: "claude"}
	if got := wid(EligibleWire(eligRows(), a, "", "")); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}

// aliasRow is an alias row over targets, as the model server sends it.
func aliasRow(id, loc string, order int, targets ...modelapi.AliasTarget) modelapi.Row {
	return modelapi.Row{ID: id, GatewayID: id, ProviderID: "alias", Family: "alias", Kind: modelapi.KindAlias,
		Location: loc, Order: order, Registered: true, Action: modelapi.ActionLaunch, State: modelapi.StateRunning, Targets: targets}
}

// TestEligibleWireAliasNeedsEveryTargetProvider pins that an alias row (whose
// own provider id is "alias") reaches an agent exactly when every one of its
// targets' providers would, by the derived rule: codex gets every alias
// whose targets are non-native; a synthesized
// claude gets the same plus its own native row; agy, which takes only its own
// native row, gets none; and an alias with no targets reaches nobody.
func TestEligibleWireAliasNeedsEveryTargetProvider(t *testing.T) {
	both := aliasRow("alias/both", "cloud", 5,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama", Location: "cloud"},
		modelapi.AliasTarget{ID: "omlx/y", Order: 2, ProviderID: "omlx", Location: "local"})
	ollamaOnly := aliasRow("alias/one", "cloud", 6,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama", Location: "cloud"})
	sushi := aliasRow("alias/sushi", "local", 7,
		modelapi.AliasTarget{ID: "sushi/p", Order: 1, ProviderID: "sushi", Location: "local"})
	rows := append(eligRows(), both, ollamaOnly, sushi)

	codex := config.Agent{Name: "codex"}
	if got := wid(EligibleWire(rows, codex, "", "")); !slices.Equal(got, []string{"a", "b", "alias/both", "alias/one", "alias/sushi"}) {
		t.Errorf("codex: %v, want [a b alias/both alias/one alias/sushi]", got)
	}
	claude := config.Agent{Name: "claude", Synthesized: true}
	if got := wid(EligibleWire(rows, claude, "", "")); !slices.Equal(got, []string{"a", "b", "c", "alias/both", "alias/one", "alias/sushi"}) {
		t.Errorf("claude: %v", got)
	}
	agy := config.Agent{Name: "agy"}
	if got := wid(EligibleWire(rows, agy, "", "")); len(got) != 0 {
		t.Errorf("agy: %v, want none", got)
	}
	empty := aliasRow("alias/none", "cloud", 8)
	if got := wid(EligibleWire([]modelapi.Row{empty}, claude, "", "")); len(got) != 0 {
		t.Errorf("an alias with no targets reached an agent: %v", got)
	}
}

// TestProviderEligible pins the four-clause rule, one clause per group of
// rows: the subscription row reaches claude alone;
// agy takes only its own native row; any other native row reaches only the
// agent of that name; and every other row reaches every agent, a synthesized
// (Synthesized) one too.
func TestProviderEligible(t *testing.T) {
	type tc struct {
		name     string
		agent    config.Agent
		provider string
		native   bool
		want     bool
	}
	var cases []tc
	add := func(a config.Agent, provider string, native, want bool) {
		cases = append(cases, tc{a.Name + "/" + provider, a, provider, native, want})
	}
	for _, n := range []string{"claude", "codex", "pi", "opencode", "copilot", "agy"} {
		add(config.Agent{Name: n}, config.SubscriptionProvider, false, n == "claude")
	}

	add(config.Agent{Name: "agy"}, "agy", true, true)
	add(config.Agent{Name: "agy"}, "claude", true, false)
	for _, p := range []string{"ollama", "sushi", "openrouter"} {
		add(config.Agent{Name: "agy"}, p, false, false)
	}
	add(config.Agent{Name: "agy"}, "agy", false, false)

	for _, n := range []string{"claude", "codex", "pi", "opencode"} {
		add(config.Agent{Name: n}, "claude", true, n == "claude")
	}
	for _, n := range []string{"copilot", "claude", "codex", "pi", "opencode"} {
		add(config.Agent{Name: n}, "copilot", true, n == "copilot")
	}
	for _, n := range []string{"codex", "claude", "pi", "opencode", "copilot"} {
		add(config.Agent{Name: n}, "codex", true, n == "codex")
	}

	for _, p := range []string{"ollama", "openrouter", "sushi", "omlx", "mtplx", "rec"} {
		for _, n := range []string{"claude", "codex", "copilot", "pi", "opencode"} {
			add(config.Agent{Name: n}, p, false, true)
		}
		add(config.Agent{Name: "pi", Synthesized: true}, p, false, true)
	}

	for _, c := range cases {
		if got := providerEligible(c.agent, c.provider, c.native); got != c.want {
			t.Errorf("%s (native=%v, synthesized=%v): got %v, want %v",
				c.name, c.native, c.agent.Synthesized, got, c.want)
		}
	}
}

// TestProviderEligibleAliasTargets pins that an alias is judged by its
// targets through the same rule: ollama and sushi targets reach every agent
// but agy, a subscription target makes the alias claude-only, and an alias
// with no targets reaches nobody.
func TestProviderEligibleAliasTargets(t *testing.T) {
	plain := aliasRow("alias/p", "local", 1,
		modelapi.AliasTarget{ID: "ollama/x", Order: 1, ProviderID: "ollama"},
		modelapi.AliasTarget{ID: "sushi/p", Order: 2, ProviderID: "sushi"})
	sub := aliasRow("alias/s", "cloud", 2,
		modelapi.AliasTarget{ID: "claude-subscription/x", Order: 1, ProviderID: config.SubscriptionProvider},
		modelapi.AliasTarget{ID: "ollama/x", Order: 2, ProviderID: "ollama"})
	none := aliasRow("alias/n", "cloud", 3)
	rows := []modelapi.Row{plain, sub, none}

	for _, c := range []struct {
		agent string
		want  []string
	}{
		{"claude", []string{"alias/p", "alias/s"}},
		{"codex", []string{"alias/p"}},
		{"pi", []string{"alias/p"}},
		{"opencode", []string{"alias/p"}},
		{"agy", nil},
	} {
		if got := wid(EligibleWire(rows, config.Agent{Name: c.agent}, "", "")); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.agent, got, c.want)
		}
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

// TestLaunchableKeepsOnlyLaunchRows pins Launchable: it keeps the rows whose
// action is launch, registered or discovered, in the order given, and drops a
// start row. A no-pin launch picks only among these rows, so a start row in
// the result would start a model nobody named.
func TestLaunchableKeepsOnlyLaunchRows(t *testing.T) {
	rows := []modelapi.Row{
		{ID: "z", Order: 2, Registered: true, Action: "launch"},
		{ID: "d", Order: 0, Discovered: true, Action: "launch"},
		{ID: "y", Order: 1, Registered: true, Action: "start"},
	}
	if got := wid(Launchable(rows)); !slices.Equal(got, []string{"z", "d"}) {
		t.Errorf("launchable %v", got)
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
