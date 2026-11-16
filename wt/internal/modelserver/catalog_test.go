package modelserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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
	c := BuildCatalog(cfg, snap, nil, testSessions(), nil, testNow)
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
	got := buildRows(rows, cfg, nil, testSessions(), nil, testNow)
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
	c := BuildCatalog(testCfg(), snap, nil, testSessions(), nil, testNow)
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
aligned_sources = ["a", "b"]
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
	c := BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), nil, testNow)
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
	c := BuildCatalog(cfg, localmodels.Snapshot{}, nil, reg, nil, testNow)
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
	r := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), nil, testNow), "openrouter/c")
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
aligned_sources = ["a", "b"]
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
		config.Model{ID: "nat/m", Family: "f", ProviderID: "nat", ModelName: "m", Native: true},
	)
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "acme", Location: config.LocationCloud},
		config.Provider{ID: "nat", Location: config.LocationCloud})
	cfg.Models[0].Cost.TimePrices = []config.TimePrice{{Label: "night", Timezone: "UTC"}}
	b, err := json.Marshal(BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), map[string]bool{"acme/m": true}, testNow))
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
// a contract the server breaks: gateway_id equals id (empty for a native row), a row is registered xor
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
		if want := id; boolean(row, "native") {
			want = ""
			if str(row, "gateway_id") != want {
				t.Errorf("%s: a native row has gateway_id %q, want empty", id, str(row, "gateway_id"))
			}
		} else if str(row, "gateway_id") != want {
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
	c := BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), nil, testNow)
	r := rowByID(t, c, "acme/m")
	if r.State != modelapi.StateBlocked || r.Action != modelapi.ActionBlock {
		t.Fatalf("state/action = %s/%s, want blocked/block", r.State, r.Action)
	}
	if !strings.Contains(r.BlockReason, "no LiteLLM mapping") || strings.Contains(r.BlockReason, "wt litellm off") {
		t.Errorf("block_reason = %q, want it to say the provider has no LiteLLM mapping", r.BlockReason)
	}
}

// TestCatalogNativeRowHasNoGatewayID pins that a native model reaches the
// wire flagged native with an empty gateway_id, since it never goes through
// the gateway, while its id stays set.
func TestCatalogNativeRowHasNoGatewayID(t *testing.T) {
	cfg := testCfg(config.Model{ID: "nat/m", Family: "f", ProviderID: "nat", ModelName: "m", Native: true})
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "nat", Location: config.LocationCloud})
	r := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), nil, testNow), "nat/m")
	if !r.Native || r.GatewayID != "" || r.ID != "nat/m" {
		t.Errorf("row = %+v, want native, empty gateway id, id nat/m", r)
	}
}

// TestCatalogOrderFollowsRegistry pins that a row's order is its registry
// index though rows sort by id, and a discovered row takes the next index, so
// a client can list models the way the registry does.
func TestCatalogOrderFollowsRegistry(t *testing.T) {
	cfg := testCfg(
		config.Model{ID: "openrouter/z", Family: "f", ProviderID: "openrouter", ModelName: "z"},
		config.Model{ID: "openrouter/a", Family: "f", ProviderID: "openrouter", ModelName: "a"},
	)
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "found", ModelID: "omlx/found", ModelName: "found", Running: true},
	}}
	c := BuildCatalog(cfg, snap, nil, testSessions(), nil, testNow)
	for id, want := range map[string]int{"openrouter/z": 0, "openrouter/a": 1, "omlx/found": 2} {
		if got := rowByID(t, c, id).Order; got != want {
			t.Errorf("%s: order = %d, want %d", id, got, want)
		}
	}
	if c.Rows[0].ID != "omlx/found" {
		t.Errorf("rows are not sorted by id: first is %q", c.Rows[0].ID)
	}
}

// TestCatalogRoutedFlag pins that a routed map sets every row's flag: true
// for an id in it, false for one not, and false everywhere for an empty
// non-nil map.
func TestCatalogRoutedFlag(t *testing.T) {
	cfg := testCfg(
		config.Model{ID: "openrouter/a", Family: "f", ProviderID: "openrouter", ModelName: "a"},
		config.Model{ID: "openrouter/b", Family: "f", ProviderID: "openrouter", ModelName: "b"},
	)
	c := BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), map[string]bool{"openrouter/a": true}, testNow)
	if a, b := rowByID(t, c, "openrouter/a").Routed, rowByID(t, c, "openrouter/b").Routed; a == nil || !*a || b == nil || *b {
		t.Errorf("routed a/b = %v/%v, want true/false", a, b)
	}
	for _, r := range BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), map[string]bool{}, testNow).Rows {
		if r.Routed == nil || *r.Routed {
			t.Errorf("%s: routed = %v, want false for an empty map", r.ID, r.Routed)
		}
	}
}

// TestCatalogRoutedUnknownWhenLiteLLMFails pins that when LiteLLM cannot be
// asked every Routed is nil and the JSON has no routed key. During a LiteLLM
// restart the catalog must not say "nothing is routed", or every client would
// start every running model.
func TestCatalogRoutedUnknownWhenLiteLLMFails(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	d.RoutedIDs = func(context.Context) (map[string]bool, error) { return nil, errors.New("proxy down") }
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour}, d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	st, b := call(t, ts, "GET", "/control/v1/catalog", "")
	if st != 200 {
		t.Fatalf("catalog = %d %s", st, b)
	}
	var c modelapi.Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Rows {
		if r.Routed != nil {
			t.Errorf("%s: routed = %v, want nil", r.ID, *r.Routed)
		}
	}
	if strings.Contains(string(b), `"routed"`) {
		t.Errorf("the JSON carries a routed key: %s", b)
	}
}

// TestResolveCarriesRouted pins R7: resolve answers a running unrouted local
// row with routed:false and omits the key when RoutedIDs errors, so a -M pin
// can tell "add the route" from "could not ask".
func TestResolveCarriesRouted(t *testing.T) {
	var fail atomic.Bool
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	d.RoutedIDs = func(context.Context) (map[string]bool, error) {
		if fail.Load() {
			return nil, errors.New("proxy down")
		}
		return map[string]bool{}, nil
	}
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour}, d)
	if err != nil {
		t.Fatal(err)
	}
	s.routedTTL = 0
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, b := call(t, ts, "GET", "/control/v1/resolve?model=omlx%2Fa", "")
	if !strings.Contains(string(b), `"routed":false`) {
		t.Errorf("resolve = %s, want routed:false", b)
	}
	fail.Store(true)
	_, b = call(t, ts, "GET", "/control/v1/resolve?model=omlx%2Fa", "")
	if strings.Contains(string(b), `"routed"`) {
		t.Errorf("resolve = %s, want no routed key", b)
	}
}

// TestRoutedIDsAreCachedBriefly pins that a burst of catalog calls asks
// LiteLLM once per routedTTL, and that a failure is cached too, so a down
// proxy is not hammered by every client poll.
func TestRoutedIDsAreCachedBriefly(t *testing.T) {
	var calls int
	var failNext bool
	clock := testNow
	f := &fakeLife{snap: serverSnap()}
	d := f.deps(serverCfg())
	d.Now = func() time.Time { return clock }
	d.RoutedIDs = func(context.Context) (map[string]bool, error) {
		calls++
		if failNext {
			return nil, errors.New("down")
		}
		return map[string]bool{}, nil
	}
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour}, d)
	if err != nil {
		t.Fatal(err)
	}
	s.routedTTL = 2 * time.Second
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	get := func() { call(t, ts, "GET", "/control/v1/catalog", "") }
	get()
	clock = clock.Add(time.Second)
	get()
	if calls != 1 {
		t.Fatalf("calls inside the ttl = %d, want 1", calls)
	}
	clock = clock.Add(2 * time.Second)
	failNext = true
	get()
	clock = clock.Add(time.Second)
	get()
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (the failure is cached too)", calls)
	}
}

// TestCatalogUnratedScoreIsKept pins that an unrated entry's aligned score and
// source count still reach the wire (they say how it compares) while the tier
// fields stay rated-only.
func TestCatalogUnratedScoreIsKept(t *testing.T) {
	ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "c"
tier = "unrated"
code = "-"
confidence = "none"
aligned_score = 44.0
aligned_sources = ["a", "b", "c"]
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"})
	r := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), nil, testNow), "openrouter/c")
	if r.Aligned == nil || *r.Aligned != 44 || r.TierSources != 3 {
		t.Errorf("aligned/sources = %v/%d, want 44/3", r.Aligned, r.TierSources)
	}
	if r.Tier != "" || r.TierCode != "" || r.Confidence != "" {
		t.Errorf("tier fields = %q/%q/%q, want empty for an unrated entry", r.Tier, r.TierCode, r.Confidence)
	}
}

// TestCatalogHostContextForUnratedRow pins that an unrated cloud entry's
// context window reaches the wire only when it is the host's own, never the
// largest row of its key.
func TestCatalogHostContextForUnratedRow(t *testing.T) {
	for basis, want := range map[string]int{"host": 200000, "largest_row": 0} {
		ix, err := tiers.Parse([]byte(`
[[models]]
provider_id = "openrouter"
model_name = "c"
tier = "unrated"
code = "-"
confidence = "none"
context = 200000
context_basis = "` + basis + `"
`))
		if err != nil {
			t.Fatal(err)
		}
		cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"})
		if got := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, ix, testSessions(), nil, testNow), "openrouter/c").Context; got != want {
			t.Errorf("basis %s: context = %d, want %d", basis, got, want)
		}
	}
}

// TestCatalogTimePricedFlag pins that a model with time-windowed prices says
// so on its price, so a client shows the figure as the one in force now.
func TestCatalogTimePricedFlag(t *testing.T) {
	in := 1.0
	cfg := testCfg(config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c",
		Cost: config.ModelCost{InputPricePerMillion: &in, TimePrices: []config.TimePrice{{Label: "night", Timezone: "UTC"}}}})
	r := rowByID(t, BuildCatalog(cfg, localmodels.Snapshot{}, nil, testSessions(), nil, testNow), "openrouter/c")
	if r.Price == nil || !r.Price.TimePriced {
		t.Errorf("price = %+v, want time_priced", r.Price)
	}
}

// TestCatalogPricingStamp pins the catalog's pricing object: present exactly
// when a model is OpenRouter-priced, carrying the newest stamp, and without
// an updated time when the prices were never refreshed.
func TestCatalogPricingStamp(t *testing.T) {
	stamp := testNow.Add(-72 * time.Hour)
	stamped := config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c", PricingUpdatedAt: stamp}
	c := BuildCatalog(testCfg(stamped), localmodels.Snapshot{}, nil, testSessions(), nil, testNow)
	if c.Pricing == nil || c.Pricing.Updated != stamp.Format(time.RFC3339) {
		t.Errorf("pricing = %+v, want updated %s", c.Pricing, stamp.Format(time.RFC3339))
	}
	other := config.Model{ID: "omlx/x", Family: "f", ProviderID: "omlx", ModelName: "x"}
	if c := BuildCatalog(testCfg(other), localmodels.Snapshot{}, nil, testSessions(), nil, testNow); c.Pricing != nil {
		t.Errorf("pricing = %+v, want nil with no OpenRouter model", c.Pricing)
	}
	unstamped := config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"}
	if c := BuildCatalog(testCfg(unstamped), localmodels.Snapshot{}, nil, testSessions(), nil, testNow); c.Pricing == nil || c.Pricing.Updated != "" {
		t.Errorf("pricing = %+v, want an empty object", c.Pricing)
	}
}
