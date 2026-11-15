package modelserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

var testNow = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func testCfg(models ...config.Model) *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal},
			{ID: "llamacpp", Location: config.LocationLocal},
		},
		Models: models,
	}
}

func testSessions() *sessionreg.Registry {
	return sessionreg.New(time.Minute, func() time.Time { return testNow })
}

func rowByID(t *testing.T, c modelapi.Catalog, id string) modelapi.Row {
	t.Helper()
	for _, r := range c.Rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", id, c.Rows)
	return modelapi.Row{}
}

func omlxEntry(id, name string, running bool) localmodels.Entry {
	return localmodels.Entry{ProviderID: "omlx", Artifact: name, ModelID: id, ModelName: name, Registered: true, Running: running, ArtifactKnown: true}
}

// TestCatalogStatesAndActions pins the state and action the wire row carries
// for a running local model, a stopped one, a cloud model and a local model
// whose provider has no start backend (blocked, with its reason).
func TestCatalogStatesAndActions(t *testing.T) {
	cfg := testCfg(
		config.Model{ID: "omlx/run", Family: "f", ProviderID: "omlx", ModelName: "run", Tags: []string{"moe"}},
		config.Model{ID: "omlx/idle", Family: "f", ProviderID: "omlx", ModelName: "idle"},
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"},
		config.Model{ID: "llamacpp/x", Family: "f", ProviderID: "llamacpp", ModelName: "x"},
	)
	snap := localmodels.Snapshot{
		Entries: []localmodels.Entry{
			omlxEntry("omlx/run", "run", true), omlxEntry("omlx/idle", "idle", false),
			{ProviderID: "llamacpp", Artifact: "x", ModelID: "llamacpp/x", ModelName: "x", Registered: true, ArtifactKnown: true},
		},
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
	}
	c := BuildCatalog(cfg, snap, nil, testSessions(), testNow)
	for _, w := range []struct{ id, state, action string }{
		{"omlx/run", modelapi.StateRunning, modelapi.ActionLaunch},
		{"omlx/idle", modelapi.StateStopped, modelapi.ActionStart},
		{"openrouter/c", modelapi.StateStopped, modelapi.ActionLaunch},
		{"llamacpp/x", modelapi.StateBlocked, modelapi.ActionBlock},
	} {
		r := rowByID(t, c, w.id)
		if r.State != w.state || r.Action != w.action {
			t.Errorf("%s: state/action = %s/%s, want %s/%s", w.id, r.State, r.Action, w.state, w.action)
		}
		if r.GatewayID != w.id {
			t.Errorf("%s: GatewayID = %q, want the model id", w.id, r.GatewayID)
		}
	}
	if r := rowByID(t, c, "llamacpp/x"); r.BlockReason == "" {
		t.Error("blocked row has no block reason")
	}
	if c.Providers["omlx"] != "ok" || c.Generated != "2026-11-15T10:00:00Z" || c.Schema != modelapi.SchemaVersion {
		t.Errorf("header = %+v", c)
	}
	for i := 1; i < len(c.Rows); i++ {
		if c.Rows[i-1].ID > c.Rows[i].ID {
			t.Errorf("rows not sorted by id: %q before %q", c.Rows[i-1].ID, c.Rows[i].ID)
		}
	}
	for _, r := range c.Rows {
		if r.Tags == nil || r.Protocols == nil {
			t.Errorf("%s: Tags/Protocols nil, would marshal as null", r.ID)
		}
	}
}

// TestCatalogSkipsProfileRows pins that buildRows drops a slot-profile row
// handed to it beside a normal row, so no profile ever reaches the wire.
func TestCatalogSkipsProfileRows(t *testing.T) {
	cfg := testCfg()
	rows := []catalog.Row{
		{Model: config.Model{ID: "slot/profile", ProviderID: "openrouter"}, Location: config.LocationCloud, Profile: &catalog.ProfileRow{}},
		{Model: config.Model{ID: "openrouter/c", ProviderID: "openrouter", ModelName: "c"}, Location: config.LocationCloud},
	}
	got := buildRows(rows, cfg, nil, testSessions(), testNow)
	if len(got) != 1 || got[0].ID != "openrouter/c" {
		t.Errorf("rows = %+v, want only openrouter/c", got)
	}
}

// TestCatalogIncludesDiscoveredLocalModels pins that an inventory entry the
// registry does not know appears as a discovered, unregistered row.
func TestCatalogIncludesDiscoveredLocalModels(t *testing.T) {
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "found", ModelID: "omlx/found", ModelName: "found", Running: true},
	}}
	c := BuildCatalog(testCfg(), snap, nil, testSessions(), testNow)
	r := rowByID(t, c, "omlx/found")
	if !r.Discovered || r.Registered || r.State != modelapi.StateRunning {
		t.Errorf("row = %+v, want discovered, unregistered, running", r)
	}
}

// TestCatalogContextOnlyWithHostBasis pins the claude driver's rule: a tier
// entry's context window is exposed only when it is the model's own host
// window, never the largest_row figure.
func TestCatalogContextOnlyWithHostBasis(t *testing.T) {
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "host"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 61.0
context = 200000
context_basis = "host"

[[models]]
provider_id = "openrouter"
model_name = "big"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 61.0
context = 1000000
context_basis = "largest_row"
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCfg(
		config.Model{ID: "openrouter/host", Family: "f", ProviderID: "openrouter", ModelName: "host"},
		config.Model{ID: "openrouter/big", Family: "f", ProviderID: "openrouter", ModelName: "big"},
		config.Model{ID: "openrouter/none", Family: "f", ProviderID: "openrouter", ModelName: "none"},
	)
	c := BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), testNow)
	h, b, n := rowByID(t, c, "openrouter/host"), rowByID(t, c, "openrouter/big"), rowByID(t, c, "openrouter/none")
	if h.Context != 200000 || h.Tier != "standard" || h.TierCode != "S" || h.Confidence != "high" || h.Aligned == nil || *h.Aligned != 61 {
		t.Errorf("host row = %+v", h)
	}
	if b.Context != 0 || b.Tier != "standard" {
		t.Errorf("largest_row row = %+v, want tier kept and Context 0", b)
	}
	if n.Tier != "" || n.Aligned != nil || n.Context != 0 {
		t.Errorf("unrated row = %+v, want no tier fields", n)
	}
}

// TestCatalogSessionsCount pins that Sessions is the registry's live count
// for the model's gateway id.
func TestCatalogSessionsCount(t *testing.T) {
	cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"})
	reg := testSessions()
	for _, id := range []string{"s1", "s2"} {
		if err := reg.Register(id, []string{"openrouter/c"}); err != nil {
			t.Fatal(err)
		}
	}
	c := BuildCatalog(cfg, localmodels.Snapshot{}, nil, reg, testNow)
	if got := rowByID(t, c, "openrouter/c").Sessions; got != 2 {
		t.Errorf("Sessions = %d, want 2", got)
	}
}

// TestCatalogPriceAndProtocols pins that the price comes from the cost table
// and the protocols from the provider's effective list.
func TestCatalogPriceAndProtocols(t *testing.T) {
	in, out := 0.3, 1.2
	cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c",
		Cost: config.ModelCost{InputPricePerMillion: &in, OutputPricePerMillion: &out}})
	r := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), testNow), "openrouter/c")
	if r.Price == nil || r.Price.Input == nil || *r.Price.Input != 0.3 || r.Price.Output == nil || *r.Price.Output != 1.2 || r.Price.Cache != nil {
		t.Errorf("price = %+v", r.Price)
	}
	if len(r.Protocols) != 1 || r.Protocols[0] != string(config.ProtocolOpenAIChat) {
		t.Errorf("protocols = %v, want the default", r.Protocols)
	}
}

// fixtureRows reads the catalog member of the contract fixture
// docs/contracts/model-api.sample.json as raw rows.
func fixtureRows(t *testing.T) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Catalog struct {
			Rows []map[string]json.RawMessage `json:"rows"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Catalog.Rows) == 0 {
		t.Fatal("the fixture has no catalog rows")
	}
	return fx.Catalog.Rows
}

// TestCatalogJSONMatchesFixture pins the marshalled rows against the contract
// fixture docs/contracts/model-api.sample.json: the keys the server marshals
// across a catalog that exercises every optional field (tier, price, context,
// block reason) are exactly the keys the fixture's rows use, so the fixture
// neither misses a key the server sends nor documents one it never sends; no
// key marshals as null.
func TestCatalogJSONMatchesFixture(t *testing.T) {
	want := map[string]bool{}
	for _, row := range fixtureRows(t) {
		for k := range row {
			want[k] = true
		}
	}
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "c"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 61.0
context = 200000
context_basis = "host"
`))
	if err != nil {
		t.Fatal(err)
	}
	in := 0.3
	cfg := testCfg(
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c",
			Cost: config.ModelCost{InputPricePerMillion: &in}},
		config.Model{ID: "acme/m", Family: "f", ProviderID: "acme", ModelName: "m"},
	)
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "acme", Location: config.LocationCloud})
	b, err := json.Marshal(BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), testNow))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, row := range got.Rows {
		for k, v := range row {
			have[k] = true
			if string(v) == "null" {
				t.Errorf("key %q marshalled as null", k)
			}
		}
	}
	for k := range want {
		if !have[k] {
			t.Errorf("the fixture documents row key %q the server never marshals", k)
		}
	}
	for k := range have {
		if !want[k] {
			t.Errorf("the server marshals row key %q the fixture does not document", k)
		}
	}
}

// TestFixtureRowsAgreeWithServerRules pins that the fixture's rows obey the
// rules the server applies, so a client built from the fixture does not learn
// a contract the server breaks: gateway_id equals id, a row is registered xor
// discovered, a blocked row is exactly a block action with a reason, a running
// row launches, and the ping is what the server answers.
func TestFixtureRowsAgreeWithServerRules(t *testing.T) {
	str := func(row map[string]json.RawMessage, k string) string {
		var v string
		_ = json.Unmarshal(row[k], &v)
		return v
	}
	boolean := func(row map[string]json.RawMessage, k string) bool {
		var v bool
		_ = json.Unmarshal(row[k], &v)
		return v
	}
	states := map[string]bool{}
	for _, row := range fixtureRows(t) {
		id := str(row, "id")
		states[str(row, "state")] = true
		if str(row, "gateway_id") != id {
			t.Errorf("%s: gateway_id %q differs from id", id, str(row, "gateway_id"))
		}
		if boolean(row, "registered") == boolean(row, "discovered") {
			t.Errorf("%s: registered and discovered must differ", id)
		}
		blocked := str(row, "state") == modelapi.StateBlocked
		if blocked != (str(row, "action") == modelapi.ActionBlock) {
			t.Errorf("%s: state %q and action %q disagree about blocking", id, str(row, "state"), str(row, "action"))
		}
		if _, has := row["block_reason"]; has != blocked {
			t.Errorf("%s: block_reason present = %v, want %v", id, has, blocked)
		}
		if str(row, "state") == modelapi.StateRunning && str(row, "action") != modelapi.ActionLaunch {
			t.Errorf("%s: a running row must launch", id)
		}
	}
	for _, s := range []string{modelapi.StateRunning, modelapi.StateLoading, modelapi.StateStopped, modelapi.StateBlocked, modelapi.StateUnknown} {
		if !states[s] {
			t.Errorf("the fixture has no row in state %q", s)
		}
	}
	srv, err := NewServer(Options{Listen: "127.0.0.1:0", Token: "t", IdleGrace: time.Minute}, (&fakeLife{}).deps(serverCfg()))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer t")
	srv.Handler().ServeHTTP(rec, req)
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Ping modelapi.Ping `json:"ping"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var got modelapi.Ping
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != fx.Ping {
		t.Errorf("server ping = %+v, fixture ping = %+v", got, fx.Ping)
	}
}

// TestCatalogUnmappedCloudRowIsBlocked pins that a cloud row whose provider
// has no LiteLLM mapping reaches the wire as blocked with a reason saying so,
// since the gateway cannot route it and a client must not offer it as ready.
func TestCatalogUnmappedCloudRowIsBlocked(t *testing.T) {
	cfg := testCfg(config.Model{ID: "acme/m", Family: "f", ProviderID: "acme", ModelName: "m"})
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "acme", Location: config.LocationCloud})
	c := BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), testNow)
	r := rowByID(t, c, "acme/m")
	if r.State != modelapi.StateBlocked || r.Action != modelapi.ActionBlock {
		t.Fatalf("state/action = %s/%s, want blocked/block", r.State, r.Action)
	}
	if !strings.Contains(r.BlockReason, "no LiteLLM mapping") || strings.Contains(r.BlockReason, "wt litellm off") {
		t.Errorf("block_reason = %q, want it to say the provider has no LiteLLM mapping", r.BlockReason)
	}
}
