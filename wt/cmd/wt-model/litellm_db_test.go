package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// fakeModelAPI is a LiteLLM model API in memory: /model/info lists the rows
// it holds (db_model true), /model/new stores one, /model/delete drops one.
// down makes every request fail with 503.
type fakeModelAPI struct {
	mu   sync.Mutex
	rows map[string]map[string]any // id -> {"model_name", "litellm_params", "model_info"}
	down bool
}

func (f *fakeModelAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/model/info":
		data := []map[string]any{}
		for id, row := range f.rows {
			info := map[string]any{"id": id, "db_model": true}
			for k, v := range row["model_info"].(map[string]any) {
				info[k] = v
			}
			data = append(data, map[string]any{"model_name": row["model_name"], "litellm_params": row["litellm_params"], "model_info": info})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "/model/new":
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		info, _ := req["model_info"].(map[string]any)
		id, _ := info["id"].(string)
		if _, dup := f.rows[id]; dup {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.rows[id] = req
	case "/model/delete":
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		if _, ok := f.rows[req["id"]]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"model not found in db"}`))
			return
		}
		delete(f.rows, req["id"])
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ids is the fake's row ids, sorted.
func (f *fakeModelAPI) ids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.rows {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// newFakeModelAPI starts a fake holding one wt row per name in names.
func newFakeModelAPI(t *testing.T, names ...string) (*fakeModelAPI, *httptest.Server) {
	t.Helper()
	f := &fakeModelAPI{rows: map[string]map[string]any{}}
	for _, n := range names {
		f.rows[litellm.DBID(n)] = map[string]any{
			"model_name":     n,
			"litellm_params": map[string]any{"model": "openai/" + n},
			"model_info":     map[string]any{litellm.ManagedKey: true},
		}
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// listFixtureYAML is the config.yaml behind the fixture's list block: one wt
// row and one hand-written row.
const listFixtureYAML = `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
`

// TestListShowsStoreColumn pins the STORE column of `wt-model litellm list`
// (#598): a local route can live in LiteLLM's database instead of
// config.yaml, and the list is the one answer to "is it routed?", so it shows
// both kinds and where each lives (db or file), with who owns it. A route
// only in the database would otherwise look unrouted.
func TestListShowsStoreColumn(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	_, srv := newFakeModelAPI(t, "omlx/qwen")
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, false, adminFromPair(srv.URL, "sk-test"), nil); err != nil {
		t.Fatal(err)
	}
	const want = "ID               STORE  OWNER\n" +
		"ollama/gemma:9b  file   wt\n" +
		"openrouter/x/y   file   hand-written\n" +
		"omlx/qwen        db     wt\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Fatalf("list =\n%s\nwant\n%s\nstderr %q", out.String(), want, errOut.String())
	}
}

// TestListJSONCarriesStore pins `wt-model litellm list --json` against the
// list block of docs/contracts/litellm-cli.sample.json: every row carries
// "store", database rows are listed beside config.yaml's, and "routed" names
// them too. The change is additive: the older keys keep their meaning, so a
// script reading "routed" or "rows[].managed" still works.
func TestListJSONCarriesStore(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	_, srv := newFakeModelAPI(t, "omlx/qwen")
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, true, adminFromPair(srv.URL, "sk-test"), nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if err := json.Unmarshal(fixture["list"], &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list --json =\n%s\nfixture =\n%s", out.String(), fixture["list"])
	}
}

// TestListWorksFromTheFileWhenTheAPIIsDown pins that list never depends on
// the proxy answering: with the model API down it prints config.yaml's rows
// and says in one stderr line that the database was not listed, never the
// key. A list that failed outright would hide the file's routes exactly when
// the user is debugging the proxy.
func TestListWorksFromTheFileWhenTheAPIIsDown(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	fake, srv := newFakeModelAPI(t, "omlx/qwen")
	fake.down = true
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, false, adminFromPair(srv.URL, "sk-secret-key"), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "omlx/qwen") || !strings.Contains(out.String(), "ollama/gemma:9b  file   wt") {
		t.Fatalf("list = %q, want the file's rows only", out.String())
	}
	lines := strings.Split(strings.TrimRight(errOut.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "wt-model: warning: database routes not listed (") || strings.Contains(errOut.String(), "sk-secret-key") {
		t.Fatalf("stderr = %q, want one warning line without the key", errOut.String())
	}
}

// TestServeStartReconcilesDatabaseRoutes pins serve's start reconcile (#598):
// a proxy restarted, or whose database was wiped, has lost every database
// route, and no start or stop is coming to put them back. Serve runs one sync
// as it starts, through the proxy pair's model API, so a running local model
// is routed again without anyone asking.
func TestServeStartReconcilesDatabaseRoutes(t *testing.T) {
	fake, srv := newFakeModelAPI(t)
	seedProxyHome(t, srv.URL, "sk-test", 0o600)
	litellmEnv(t, "model_list: []\n"+
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"+
		"general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n")
	probe := func(*config.Config) localmodels.Snapshot {
		return localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
			Entries: []localmodels.Entry{{
				ProviderID: "ollama", ModelName: "gemma4:9b", ModelID: "ollama/gemma4:9b",
				Registered: true, Running: true,
			}},
		}
	}
	rt := testRuntime(t)
	rt.StartSync = serveStartSync(probe)
	readErr, _, stop := serveUntilListeningWith(t, rt, writeToken(t, 0o600))
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(fake.ids(), "wt:ollama/gemma4:9b") {
		if time.Now().After(deadline) {
			_ = stop()
			t.Fatalf("database rows %v, want wt:ollama/gemma4:9b; stderr %q", fake.ids(), readErr())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if strings.Contains(readErr(), "sk-test") {
		t.Fatalf("stderr shows the key: %q", readErr())
	}
}

// TestServeStartSyncFailureIsAWarning pins that the start reconcile never
// stops serving: a config.yaml it cannot read is one warning line on stderr
// and the server goes on listening.
func TestServeStartSyncFailureIsAWarning(t *testing.T) {
	_, srv := newFakeModelAPI(t)
	seedProxyHome(t, srv.URL, "sk-test", 0o600)
	litellmEnv(t, "model_list: {not: a list}\n")
	rt := testRuntime(t)
	rt.StartSync = serveStartSync(func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} })
	readErr, _, stop := serveUntilListeningWith(t, rt, writeToken(t, 0o600))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(readErr(), "wt-model: warning: LiteLLM routes not synced at start") {
		if time.Now().After(deadline) {
			_ = stop()
			t.Fatalf("stderr = %q, want the start sync's warning", readErr())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestSyncDryRunReadsTheDatabaseAndWritesNothing pins `wt-model litellm sync
// --dry-run` with the proxy pair's model API (#598): it lists the database
// and writes nothing there (no route added before the real sync), and once
// the real sync has put a running model's route in the database, the dry run
// no longer offers to route it. Before, a dry run blind to the database
// listed every running database-hosted route as "would route" on every run.
func TestSyncDryRunReadsTheDatabaseAndWritesNothing(t *testing.T) {
	fake, srv := newFakeModelAPI(t)
	seedConfig(t, false)
	litellmEnv(t, "model_list: []\n"+
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"+
		"general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n")
	h := testHost()
	h.adminFor = func(*config.Config) *litellm.AdminClient { return adminFromPair(srv.URL, "sk-test") }
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{{
			ProviderID: "ollama", ModelName: "gemma4:9b", ModelID: "ollama/gemma4:9b",
			Registered: true, Running: true,
		}},
	})
	out, _, err := execWTModel(t, h, "litellm", "sync", "--dry-run")
	if err != nil || !strings.Contains(out, "ollama/gemma4:9b: would route\n") || len(fake.ids()) != 0 {
		t.Fatalf("first dry run: err %v stdout %q rows %v, want a route offered and nothing written", err, out, fake.ids())
	}
	if _, _, err := execWTModel(t, h, "litellm", "sync"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.ids(), []string{"wt:ollama/gemma4:9b"}) {
		t.Fatalf("database rows %v after sync", fake.ids())
	}
	out, _, err = execWTModel(t, h, "litellm", "sync", "--dry-run")
	if err != nil || strings.Contains(out, "ollama/gemma4:9b") {
		t.Fatalf("second dry run: err %v stdout %q, want nothing for the database route", err, out)
	}
}
