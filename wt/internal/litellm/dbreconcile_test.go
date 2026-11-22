package litellm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func managedRow(id, name string, params map[string]any) DBModel {
	return DBModel{ID: id, ModelName: name, Params: params, Info: map[string]any{ManagedKey: true}}
}

// stored returns m as wt's Add leaves it in the database: Info carries the
// route hash.
func stored(m DBModel) DBModel {
	info := map[string]any{ParamsHashKey: routeHash(m)}
	for k, v := range m.Info {
		info[k] = v
	}
	m.Info = info
	return m
}

func planIDs(ms []DBModel) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// TestPlanDBAddsMissing checks that a desired row absent from the database is
// planned as an Add, sorted by id, so a route the registry wants gets created
// in a stable order.
func TestPlanDBAddsMissing(t *testing.T) {
	desired := []DBModel{
		managedRow("wt:b", "b", map[string]any{"model": "ollama/b"}),
		managedRow("wt:a", "a", map[string]any{"model": "ollama/a"}),
	}
	p := PlanDB(desired, nil)
	if got := planIDs(p.Add); !reflect.DeepEqual(got, []string{"wt:a", "wt:b"}) {
		t.Fatalf("Add %v", got)
	}
	if len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("plan %+v", p)
	}
}

// TestPlanDBDeletesManagedStrays checks that a wt-managed database row the
// registry no longer wants is deleted (sorted by id); otherwise a removed
// model would keep routing forever.
func TestPlanDBDeletesManagedStrays(t *testing.T) {
	current := []DBModel{
		managedRow("wt:z", "z", map[string]any{"model": "ollama/z"}),
		managedRow("wt:y", "y", map[string]any{"model": "ollama/y"}),
	}
	p := PlanDB(nil, current)
	if !reflect.DeepEqual(p.Delete, []string{"wt:y", "wt:z"}) {
		t.Fatalf("Delete %v", p.Delete)
	}
	if len(p.Add) != 0 || len(p.Update) != 0 {
		t.Fatalf("plan %+v", p)
	}
}

// TestPlanDBIgnoresUnmanagedDatabaseRows checks that rows without
// wt_managed: true (added through the LiteLLM UI or by another owner, or
// with a non-bool marker) are never deleted or updated: wt owns only what it
// marked.
func TestPlanDBIgnoresUnmanagedDatabaseRows(t *testing.T) {
	current := []DBModel{
		{ID: "ui-row", ModelName: "x", Params: map[string]any{"model": "openai/x"}, Info: map[string]any{}},
		{ID: "wt:fake", ModelName: "y", Params: map[string]any{"model": "openai/y"}, Info: map[string]any{ManagedKey: "true"}},
		{ID: "nil-info", ModelName: "z", Params: map[string]any{"model": "openai/z"}},
	}
	p := PlanDB(nil, current)
	if !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// TestPlanDBUpdatesChangedParams checks that a managed row stored with the
// hash of other params or another model_name is planned as an Update carrying
// the desired row, so a changed registry entry reaches the database.
func TestPlanDBUpdatesChangedParams(t *testing.T) {
	desired := []DBModel{
		managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "api_base": "http://new"}),
		managedRow("wt:b", "b2", map[string]any{"model": "ollama/b"}),
	}
	current := []DBModel{
		stored(managedRow("wt:b", "b", map[string]any{"model": "ollama/b"})),
		stored(managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "api_base": "http://old"})),
	}
	p := PlanDB(desired, current)
	if got := planIDs(p.Update); !reflect.DeepEqual(got, []string{"wt:a", "wt:b"}) {
		t.Fatalf("Update %v", got)
	}
	if p.Update[0].Params["api_base"] != "http://new" || p.Update[1].ModelName != "b2" {
		t.Fatalf("Update carries %+v, want desired rows", p.Update)
	}
	if len(p.Add) != 0 || len(p.Delete) != 0 {
		t.Fatalf("plan %+v", p)
	}
}

// TestPlanDBUpdatesManagedRowWithoutHash checks that a managed row carrying
// no wt_params_sha (written before the hash existed, or by hand) is replaced:
// its stored params cannot be trusted to match.
func TestPlanDBUpdatesManagedRowWithoutHash(t *testing.T) {
	row := managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})
	p := PlanDB([]DBModel{row}, []DBModel{row})
	if got := planIDs(p.Update); !reflect.DeepEqual(got, []string{"wt:a"}) {
		t.Fatalf("plan %+v, want one Update", p)
	}
}

// TestPlanDBIsEmptyWhenEqual checks that a stored row carrying the desired
// row's hash plans nothing, even when its params read back differently, so a
// repeated sync makes no database writes.
func TestPlanDBIsEmptyWhenEqual(t *testing.T) {
	want := managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "api_key": "os.environ/K"})
	got := stored(want)
	got.Params = map[string]any{"model": "ollama/a"} // api_key stripped by /model/info
	if p := PlanDB([]DBModel{want}, []DBModel{got}); !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// TestRouteHashIgnoresNumberTypeAndKeyOrder checks that a row decoded from
// YAML (int 4096, []any, nested maps) hashes the same as the same row decoded
// from JSON (float64 4096, other key order), and that model_name and every
// param are part of the hash. Without that, every sync would replace every
// row, or miss a change.
func TestRouteHashIgnoresNumberTypeAndKeyOrder(t *testing.T) {
	fromYAML := managedRow("wt:a", "a", map[string]any{
		"model":                  "ollama/a",
		"max_tokens":             4096,
		"additional_drop_params": []any{"temperature", "top_p"},
		"extra":                  map[string]any{"n": 2, "b": true},
	})
	var params map[string]any
	if err := json.Unmarshal([]byte(`{"extra":{"b":true,"n":2.0},"additional_drop_params":["temperature","top_p"],"max_tokens":4096.0,"model":"ollama/a"}`), &params); err != nil {
		t.Fatal(err)
	}
	fromJSON := managedRow("wt:a", "a", params)
	h := routeHash(fromYAML)
	if h == "" || h != routeHash(fromJSON) {
		t.Fatalf("hash %q vs %q", h, routeHash(fromJSON))
	}
	renamed := fromJSON
	renamed.ModelName = "b"
	if routeHash(renamed) == h {
		t.Fatal("model_name is not part of the hash")
	}
	if routeHash(managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "max_tokens": 4097,
		"additional_drop_params": []any{"temperature", "top_p"}, "extra": map[string]any{"n": 2, "b": true}})) == h {
		t.Fatal("a changed param kept the hash")
	}
	if p := PlanDB([]DBModel{fromYAML}, []DBModel{stored(fromJSON)}); !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// fakeDBServer is a LiteLLM model API holding rows in memory, answering a
// duplicate id and a missing id with the real 1.109.1 shapes (HTTP 500
// "Failed to add model to db", HTTP 400 "not found in db"). Like the real
// /model/info it drops api_key from litellm_params and masks other
// sensitive-looking values. addFailBody, when set, answers every /model/new
// with that body as an HTTP 500 and stores nothing; calls counts requests.
// swallowAdds answers /model/new with 200 and stores nothing (a database that
// is off but says yes, Review Focus 5); log holds "METHOD /path" per request.
// failList answers /model/info with 503; failDeleteID answers a delete of
// that id with 503 and keeps the row. failAddID answers an add of that id
// with HTTP 500 and abortAddID drops the connection on it (a transport
// error); either stores nothing.
type fakeDBServer struct {
	mu           sync.Mutex
	rows         map[string]map[string]any // id -> {"model_name", "litellm_params", "model_info"}
	addFailBody  string
	swallowAdds  bool
	failList     bool
	failDeleteID string
	failAddID    string
	abortAddID   string
	calls        int
	log          []string
}

func maskedParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		lk := strings.ToLower(k)
		switch {
		case lk == "api_key":
			continue
		case strings.Contains(lk, "secret") || strings.Contains(lk, "token") || strings.Contains(lk, "key"):
			s, _ := v.(string)
			if len(s) > 2 {
				s = s[:2]
			}
			out[k] = s + "****"
		default:
			out[k] = v
		}
	}
	return out
}

func (f *fakeDBServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	b, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/model/info":
		if f.failList {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"service unavailable"}`))
			return
		}
		var data []map[string]any
		for _, row := range f.rows {
			p, _ := row["litellm_params"].(map[string]any)
			data = append(data, map[string]any{
				"model_name": row["model_name"], "litellm_params": maskedParams(p), "model_info": row["model_info"],
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "/model/new":
		if f.addFailBody != "" {
			w.WriteHeader(500)
			w.Write([]byte(f.addFailBody))
			return
		}
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		if f.swallowAdds {
			w.Write([]byte(`{"model_id":"ignored"}`))
			return
		}
		info, _ := req["model_info"].(map[string]any)
		id, _ := info["id"].(string)
		if id != "" && id == f.abortAddID {
			panic(http.ErrAbortHandler)
		}
		if id != "" && id == f.failAddID {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":{"message":"database unavailable"}}`))
			return
		}
		if _, dup := f.rows[id]; dup {
			w.WriteHeader(500)
			w.Write([]byte(realDuplicateBody))
			return
		}
		info["db_model"] = true
		f.rows[id] = req
		w.Write([]byte(`{"model_id":"` + id + `"}`))
	case "/model/delete":
		var req map[string]string
		_ = json.Unmarshal(b, &req)
		if req["id"] == f.failDeleteID {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"service unavailable"}`))
			return
		}
		if _, ok := f.rows[req["id"]]; !ok {
			w.WriteHeader(400)
			w.Write([]byte(strings.Replace(realMissingBody, "wt:ollama/x", req["id"], 1)))
			return
		}
		delete(f.rows, req["id"])
		w.Write([]byte(`{"message":"deleted"}`))
	default:
		w.WriteHeader(404)
	}
}

func newFakeDB(t *testing.T) (*fakeDBServer, *AdminClient) {
	t.Helper()
	f := &fakeDBServer{rows: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
}

// seed stores m as given (Info verbatim plus id and db_model).
func (f *fakeDBServer) seed(m DBModel) {
	info := map[string]any{"id": m.ID, "db_model": true}
	for k, v := range m.Info {
		info[k] = v
	}
	f.rows[m.ID] = map[string]any{"model_name": m.ModelName, "litellm_params": m.Params, "model_info": info}
}

// TestApplyDBTwoClientsConverge checks that two clients applying the same
// plan at once (two syncs racing) both succeed and leave exactly one row per
// id: the loser's Add meets LiteLLM's real duplicate answer (HTTP 500, not an
// "already exists" message), re-lists, finds the row by its hash (the listed
// params have api_key stripped) and counts it as converged instead of
// failing the sync.
func TestApplyDBTwoClientsConverge(t *testing.T) {
	for range 20 {
		f, c := newFakeDB(t)
		f.seed(stored(managedRow("wt:u", "u", map[string]any{"model": "ollama/u", "api_base": "http://old"})))
		p := DBPlan{
			Add: []DBModel{managedRow("wt:a", "a", map[string]any{
				"model": "openai/a", "max_tokens": 4096, "api_key": "os.environ/A_KEY"})},
			Update: []DBModel{managedRow("wt:u", "u", map[string]any{
				"model": "ollama/u", "api_base": "http://new", "api_key": "os.environ/U_KEY"})},
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, _, errs[i] = c.ApplyDB(context.Background(), p)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("client %d: %v", i, err)
			}
		}
		got, err := c.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("rows %+v, want 2", got)
		}
		if again := PlanDB(append(p.Add, p.Update...), got); !again.Empty() {
			t.Fatalf("not converged: %+v", again)
		}
	}
}

// TestApplyDBKeyedRowRoundTrip checks a row with an api_key reference against
// a server that strips api_key from /model/info, as LiteLLM 1.109.1 does: the
// applied row plans nothing on the next sync, and a changed api_key reference
// plans an Update. Comparing params would replace every keyed row on every
// sync.
func TestApplyDBKeyedRowRoundTrip(t *testing.T) {
	_, c := newFakeDB(t)
	ctx := context.Background()
	want := managedRow("wt:k", "k", map[string]any{"model": "openai/k", "api_key": "os.environ/K1"})
	if _, _, _, err := c.ApplyDB(ctx, PlanDB([]DBModel{want}, nil)); err != nil {
		t.Fatal(err)
	}
	got, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Params["api_key"] != nil {
		t.Fatalf("fake should strip api_key: %+v", got)
	}
	if p := PlanDB([]DBModel{want}, got); !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
	changed := managedRow("wt:k", "k", map[string]any{"model": "openai/k", "api_key": "os.environ/K2"})
	if p := PlanDB([]DBModel{changed}, got); !reflect.DeepEqual(planIDs(p.Update), []string{"wt:k"}) {
		t.Fatalf("plan %+v, want Update of wt:k", p)
	}
}

// TestApplyDBDeleteOfAbsentRowIsNoop checks that deleting a row another sync
// already removed (LiteLLM's real HTTP 400 "not found in db") is not an
// error, so a second sync racing the first does not fail.
func TestApplyDBDeleteOfAbsentRowIsNoop(t *testing.T) {
	_, c := newFakeDB(t)
	_, _, deleted, err := c.ApplyDB(context.Background(), DBPlan{Delete: []string{"wt:gone"}})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d, want 1", deleted)
	}
}

// TestApplyDBRefusesIDsWithoutPrefix checks that a Delete or Update of an id
// not starting with "wt:" is refused without any HTTP call: wt touches no row
// it did not name, whatever plan it is handed.
func TestApplyDBRefusesIDsWithoutPrefix(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(managedRow("ui-row", "x", map[string]any{"model": "openai/x"}))
	_, updated, deleted, err := c.ApplyDB(context.Background(), DBPlan{
		Delete: []string{"ui-row"},
		Update: []DBModel{managedRow("other", "x", map[string]any{"model": "openai/y"})},
	})
	if err == nil || updated != 0 || deleted != 0 {
		t.Fatalf("updated %d deleted %d err %v", updated, deleted, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "ui-row") || !strings.Contains(msg, "other") {
		t.Fatalf("error %q should name both ids", msg)
	}
	if f.calls != 0 {
		t.Fatalf("%d HTTP calls, want 0", f.calls)
	}
}

// TestApplyDBCountsEachKind checks the returned counts and the effect on the
// server for one plan with an Add, an Update and a Delete, and that every
// added row carries its route hash.
func TestApplyDBCountsEachKind(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(stored(managedRow("wt:u", "u", map[string]any{"model": "ollama/old"})))
	f.seed(stored(managedRow("wt:d", "d", map[string]any{"model": "ollama/d"})))
	add := managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})
	added, updated, deleted, err := c.ApplyDB(context.Background(), DBPlan{
		Add:    []DBModel{add},
		Update: []DBModel{managedRow("wt:u", "u", map[string]any{"model": "ollama/new"})},
		Delete: []string{"wt:d"},
	})
	if err != nil || added != 1 || updated != 1 || deleted != 1 {
		t.Fatalf("added %d updated %d deleted %d err %v", added, updated, deleted, err)
	}
	if _, ok := f.rows["wt:d"]; ok {
		t.Fatal("wt:d still present")
	}
	if p, _ := f.rows["wt:u"]["litellm_params"].(map[string]any); p["model"] != "ollama/new" {
		t.Fatalf("wt:u params %v", f.rows["wt:u"]["litellm_params"])
	}
	if info, _ := f.rows["wt:a"]["model_info"].(map[string]any); info[ParamsHashKey] != routeHash(add) || info[ManagedKey] != true {
		t.Fatalf("wt:a model_info %v", info)
	}
	if add.Info[ParamsHashKey] != nil {
		t.Fatal("ApplyDB stamped the caller's Info map")
	}
}

// TestApplyDBAddFailureNotInListIsError checks that an Add whose failure is
// not explained by an equal row already present is returned, with the id,
// rather than counted as converged.
func TestApplyDBAddFailureNotInListIsError(t *testing.T) {
	f, c := newFakeDB(t)
	f.addFailBody = `{"error":{"message":"database unavailable"}}`
	added, _, _, err := c.ApplyDB(context.Background(), DBPlan{
		Add: []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})},
	})
	if err == nil || added != 0 || !strings.Contains(err.Error(), "wt:a") {
		t.Fatalf("added %d err %v", added, err)
	}
}

// TestApplyDBStoreOffSurvivesTheJoin checks that LiteLLM's real answer with
// store_model_in_db off (HTTP 500 naming STORE_MODEL_IN_DB) still satisfies
// errors.Is(err, ErrDBModelsOff) through ApplyDB's joined error: the caller
// turns it into the "database routes are off" message.
func TestApplyDBStoreOffSurvivesTheJoin(t *testing.T) {
	f, c := newFakeDB(t)
	f.addFailBody = realStoreOffBody
	_, _, _, err := c.ApplyDB(context.Background(), DBPlan{
		Add: []DBModel{
			managedRow("wt:a", "a", map[string]any{"model": "ollama/a"}),
			managedRow("wt:b", "b", map[string]any{"model": "ollama/b"}),
		},
	})
	if !errors.Is(err, ErrDBModelsOff) {
		t.Fatalf("err %v, want ErrDBModelsOff", err)
	}
}

// TestApplyDBAddOfDifferentExistingRowIsError checks that a duplicate-id
// answer counts as converged only when the stored row is wt-managed and
// carries the wanted hash: a different row, or an unmarked one with the same
// hash, under the same id is an error.
func TestApplyDBAddOfDifferentExistingRowIsError(t *testing.T) {
	want := managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})
	unmarked := stored(want)
	unmarked.Info = map[string]any{ParamsHashKey: routeHash(want)}
	for _, tc := range []struct {
		name string
		row  DBModel
	}{
		{"other params", stored(managedRow("wt:a", "a", map[string]any{"model": "ollama/other"}))},
		{"same hash, no marker", unmarked},
	} {
		f, c := newFakeDB(t)
		f.seed(tc.row)
		added, _, _, err := c.ApplyDB(context.Background(), DBPlan{Add: []DBModel{want}})
		if err == nil || added != 0 || !strings.Contains(err.Error(), "wt:a") {
			t.Errorf("%s: added %d err %v", tc.name, added, err)
		}
	}
}

// TestApplyDBUpdateReportsMissingRouteWhenReAddFails checks that an Update
// whose Delete succeeded but whose Add failed returns an error naming the id
// and saying the route is missing, so the caller can warn that the model has
// no route until the next sync.
func TestApplyDBUpdateReportsMissingRouteWhenReAddFails(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(stored(managedRow("wt:u", "u", map[string]any{"model": "ollama/old"})))
	f.addFailBody = `{"error":{"message":"database unavailable"}}`
	_, updated, _, err := c.ApplyDB(context.Background(), DBPlan{
		Update: []DBModel{managedRow("wt:u", "u", map[string]any{"model": "ollama/new"})},
	})
	if err == nil || updated != 0 {
		t.Fatalf("updated %d err %v", updated, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "wt:u") || !strings.Contains(msg, "missing") {
		t.Fatalf("error %q should name wt:u and say the route is missing", msg)
	}
	if _, ok := f.rows["wt:u"]; ok {
		t.Fatal("wt:u should be gone after the failed re-add")
	}
}

// TestPlanDBSkipsManagedRowsWithoutTheWtPrefix checks that a row marked
// wt_managed whose id does not start with "wt:" is neither deleted nor
// updated: ApplyDB refuses such ids, so planning them would make every sync
// fail on the same row (the route write reports it once as a warning).
func TestPlanDBSkipsManagedRowsWithoutTheWtPrefix(t *testing.T) {
	odd := stored(managedRow("ui-made", "ollama/a", map[string]any{"model": "ollama/a"}))
	if p := PlanDB(nil, []DBModel{odd}); !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
	want := managedRow("wt:ollama/a", "ollama/a", map[string]any{"model": "ollama/a"})
	p := PlanDB([]DBModel{want}, []DBModel{odd})
	if got := planIDs(p.Add); !reflect.DeepEqual(got, []string{"wt:ollama/a"}) || len(p.Update)+len(p.Delete) != 0 {
		t.Fatalf("plan %+v, want only the wt: add", p)
	}
}

// TestApplyDBStopsAfterTransportError checks that a proxy that drops the
// connection costs one request, not one per item: ApplyDB runs under the
// config.yaml lock, and trying every remaining item against a dead proxy
// would hold it for one timeout each.
func TestApplyDBStopsAfterTransportError(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	c := &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
	p := DBPlan{
		Delete: []string{"wt:a", "wt:b"},
		Update: []DBModel{managedRow("wt:u", "u", map[string]any{"model": "ollama/u"})},
		Add:    []DBModel{managedRow("wt:n", "n", map[string]any{"model": "ollama/n"})},
	}
	_, _, _, err := c.ApplyDB(context.Background(), p)
	if err == nil || !isTransport(err) {
		t.Fatalf("err %v, want a transport error", err)
	}
	if strings.Contains(err.Error(), testMasterKey) {
		t.Fatal("the error carries the master key")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d requests, want 1", calls)
	}
}
