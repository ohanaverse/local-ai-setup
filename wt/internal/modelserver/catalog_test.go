package modelserver

import (
	"encoding/json"
	"os"
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

// TestCatalogJSONMatchesFixture pins the marshalled row against the contract
// fixture docs/contracts/model-api.sample.json: every key the fixture's rows
// use for a row of the same shape must exist in what the server marshals.
func TestCatalogJSONMatchesFixture(t *testing.T) {
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
	// Keys every fixture row has, whatever its optional fields.
	required := map[string]bool{}
	for k := range fx.Catalog.Rows[0] {
		required[k] = true
	}
	for _, row := range fx.Catalog.Rows[1:] {
		for k := range required {
			if _, ok := row[k]; !ok {
				delete(required, k)
			}
		}
	}
	cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"})
	b, err := json.Marshal(BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), testNow))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for k := range required {
		if _, ok := got.Rows[0][k]; !ok {
			t.Errorf("marshalled row lacks fixture key %q", k)
		}
	}
	for k, v := range got.Rows[0] {
		if string(v) == "null" {
			t.Errorf("key %q marshalled as null", k)
		}
	}
}
