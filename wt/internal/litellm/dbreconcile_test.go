package litellm

import (
	"context"
	"encoding/json"
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
	if len(p.Add) != 0 || len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// TestPlanDBUpdatesChangedParams checks that a managed row whose params or
// model_name differ from the desired one is planned as an Update carrying the
// desired row, so a changed registry entry reaches the database.
func TestPlanDBUpdatesChangedParams(t *testing.T) {
	desired := []DBModel{
		managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "api_base": "http://new"}),
		managedRow("wt:b", "b2", map[string]any{"model": "ollama/b"}),
	}
	current := []DBModel{
		managedRow("wt:b", "b", map[string]any{"model": "ollama/b"}),
		managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "api_base": "http://old"}),
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

// TestPlanDBIsEmptyWhenEqual checks that identical rows plan nothing, so a
// repeated sync makes no database writes.
func TestPlanDBIsEmptyWhenEqual(t *testing.T) {
	rows := []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})}
	p := PlanDB(rows, rows)
	if len(p.Add) != 0 || len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// TestPlanDBIsEmptyWhenEqualAfterJSONRoundTrip checks that a desired row
// decoded from YAML (int 4096, []any, nested maps) equals the same row as
// List returns it from JSON (float64 4096, different key order). Without
// normalizing, every sync would delete and re-add every row.
func TestPlanDBIsEmptyWhenEqualAfterJSONRoundTrip(t *testing.T) {
	desired := []DBModel{managedRow("wt:a", "a", map[string]any{
		"model":                  "ollama/a",
		"max_tokens":             4096,
		"additional_drop_params": []any{"temperature", "top_p"},
		"extra":                  map[string]any{"n": 2, "b": true},
	})}
	var params map[string]any
	if err := json.Unmarshal([]byte(`{"extra":{"b":true,"n":2.0},"additional_drop_params":["temperature","top_p"],"max_tokens":4096.0,"model":"ollama/a"}`), &params); err != nil {
		t.Fatal(err)
	}
	current := []DBModel{managedRow("wt:a", "a", params)}
	p := PlanDB(desired, current)
	if len(p.Add) != 0 || len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// fakeDBServer is a LiteLLM model API holding rows in memory, answering a
// duplicate id and a missing id with the real 1.109.1 shapes (HTTP 500
// "Failed to add model to db", HTTP 400 "not found in db"). failAdd, when
// set, answers every /model/new with an unrelated 500 and stores nothing.
type fakeDBServer struct {
	mu      sync.Mutex
	rows    map[string]map[string]any // id -> {"model_name", "litellm_params", "model_info"}
	failAdd bool
}

func (f *fakeDBServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/model/info":
		var data []map[string]any
		for _, row := range f.rows {
			data = append(data, row)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "/model/new":
		if f.failAdd {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":{"message":"database unavailable"}}`))
			return
		}
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		info, _ := req["model_info"].(map[string]any)
		id, _ := info["id"].(string)
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
// "already exists" message), re-lists, finds the equal row and counts it as
// converged instead of failing the sync.
func TestApplyDBTwoClientsConverge(t *testing.T) {
	for range 20 {
		f, c := newFakeDB(t)
		old := managedRow("wt:u", "u", map[string]any{"model": "ollama/u", "api_base": "http://old"})
		f.seed(old)
		p := DBPlan{
			Add:    []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a", "max_tokens": 4096})},
			Update: []DBModel{managedRow("wt:u", "u", map[string]any{"model": "ollama/u", "api_base": "http://new"})},
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

// TestApplyDBCountsEachKind checks the returned counts and the effect on the
// server for one plan with an Add, an Update and a Delete.
func TestApplyDBCountsEachKind(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(managedRow("wt:u", "u", map[string]any{"model": "ollama/old"}))
	f.seed(managedRow("wt:d", "d", map[string]any{"model": "ollama/d"}))
	added, updated, deleted, err := c.ApplyDB(context.Background(), DBPlan{
		Add:    []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})},
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
}

// TestApplyDBAddFailureNotInListIsError checks that an Add whose failure is
// not explained by an equal row already present is returned, with the id,
// rather than counted as converged.
func TestApplyDBAddFailureNotInListIsError(t *testing.T) {
	f, c := newFakeDB(t)
	f.failAdd = true
	added, _, _, err := c.ApplyDB(context.Background(), DBPlan{
		Add: []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})},
	})
	if err == nil || added != 0 || !strings.Contains(err.Error(), "wt:a") {
		t.Fatalf("added %d err %v", added, err)
	}
}

// TestApplyDBAddOfDifferentExistingRowIsError checks that a duplicate-id
// answer counts as converged only when the stored row equals the wanted one:
// a different row under the same id is an error.
func TestApplyDBAddOfDifferentExistingRowIsError(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(managedRow("wt:a", "a", map[string]any{"model": "ollama/other"}))
	added, _, _, err := c.ApplyDB(context.Background(), DBPlan{
		Add: []DBModel{managedRow("wt:a", "a", map[string]any{"model": "ollama/a"})},
	})
	if err == nil || added != 0 || !strings.Contains(err.Error(), "wt:a") {
		t.Fatalf("added %d err %v", added, err)
	}
}

// TestApplyDBUpdateReportsMissingRouteWhenReAddFails checks that an Update
// whose Delete succeeded but whose Add failed returns an error naming the id
// and saying the route is missing, so the caller can warn that the model has
// no route until the next sync.
func TestApplyDBUpdateReportsMissingRouteWhenReAddFails(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(managedRow("wt:u", "u", map[string]any{"model": "ollama/old"}))
	f.failAdd = true
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
