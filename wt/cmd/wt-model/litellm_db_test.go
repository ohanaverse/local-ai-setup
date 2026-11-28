package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// fakeModelAPI is a LiteLLM model API in memory: /model/info lists the rows
// it holds (db_model true), /model/new stores one, /model/delete drops one.
// /health/liveliness answers 200. down makes every request fail with 503,
// failList only the listing, and hangList holds the listing until the
// request ends.
type fakeModelAPI struct {
	mu       sync.Mutex
	rows     map[string]map[string]any // id -> {"model_name", "litellm_params", "model_info"}
	down     bool
	failList bool
	hangList bool
}

func (f *fakeModelAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hang := f.hangList && r.URL.Path == "/model/info"
	f.mu.Unlock()
	if hang {
		<-r.Context().Done()
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down || f.failList && r.URL.Path == "/model/info" {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/health/liveliness":
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

// TestListShowsAMigratedCloudRowAsDB pins decision 5 of #694: a cloud model wt
// moved to LiteLLM's database reads STORE db and OWNER wt, and a hand-written
// config.yaml row under a cloud name keeps reading file and hand-written, so
// the column still answers "what would a restart touch".
func TestListShowsAMigratedCloudRowAsDB(t *testing.T) {
	litellmEnv(t, "model_list:\n  - model_name: hand/cloud\n    litellm_params: {model: openrouter/hand}\n")
	_, srv := newFakeModelAPI(t, "openrouter/x/y")
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, false, adminFromPair(srv.URL, "sk-test"), nil); err != nil {
		t.Fatal(err)
	}
	const want = "ID              STORE  OWNER\n" +
		"hand/cloud      file   hand-written\n" +
		"openrouter/x/y  db     wt\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Fatalf("list =\n%s\nwant\n%s\nstderr %q", out.String(), want, errOut.String())
	}
}

// seedAliasRow stores one alias row (wt_alias_managed, under wta:alias/) of
// alias for target at order, as ApplyAliasDB would leave it.
func (f *fakeModelAPI) seedAliasRow(alias, target string, order int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := litellm.DBAliasIDPrefix + strings.TrimPrefix(alias, "alias/") + "/0123abcd/" + target
	f.rows[id] = map[string]any{
		"model_name":     alias,
		"litellm_params": map[string]any{"model": "openai/" + target, "order": order},
		"model_info":     map[string]any{litellm.AliasManagedKey: true, litellm.AliasKey: alias, litellm.AliasTargetKey: target},
	}
}

// listFixtureAPI is the database behind the fixture's list block: one wt
// model row and alias/x, a group of two targets.
func listFixtureAPI(t *testing.T) *httptest.Server {
	t.Helper()
	fake, srv := newFakeModelAPI(t, "omlx/qwen")
	fake.seedAliasRow("alias/x", "omlx/qwen", 2)
	fake.seedAliasRow("alias/x", "openrouter/x/y", 1)
	return srv
}

// TestListShowsAliasTargetAndOrder pins the alias rows of `wt-model litellm
// list` (#596): one line per deployment of an alias, with the TARGET it
// routes to and its ORDER, so a group's fallback order is visible where
// routes are reviewed. Model rows show "-" in both columns.
func TestListShowsAliasTargetAndOrder(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	srv := listFixtureAPI(t)
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, false, adminFromPair(srv.URL, "sk-test"), nil); err != nil {
		t.Fatal(err)
	}
	const want = "ID               STORE  OWNER         TARGET          ORDER\n" +
		"ollama/gemma:9b  file   wt            -               -\n" +
		"openrouter/x/y   file   hand-written  -               -\n" +
		"omlx/qwen        db     wt            -               -\n" +
		"alias/x          db     wt            openrouter/x/y  1\n" +
		"alias/x          db     wt            omlx/qwen       2\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Fatalf("list =\n%s\nwant\n%s\nstderr %q", out.String(), want, errOut.String())
	}
}

// TestListJSONCarriesKindTargetOrder pins the additive alias keys of `list
// --json` (#596): every row has kind (model or alias), an alias deployment
// carries target and order and repeats its id once per target, and routed
// names the alias once. A script reading routed or rows[].id keeps working.
func TestListJSONCarriesKindTargetOrder(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	srv := listFixtureAPI(t)
	var out, errOut bytes.Buffer
	if err := runLitellmList(t.Context(), &out, &errOut, true, adminFromPair(srv.URL, "sk-test"), nil); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Routed []string         `json:"routed"`
		Rows   []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, id := range doc.Routed {
		if id == "alias/x" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("routed %v, want alias/x once", doc.Routed)
	}
	var aliasRows []string
	for _, r := range doc.Rows {
		switch r["kind"] {
		case "model":
			if _, ok := r["target"]; ok {
				t.Fatalf("model row %v carries target", r)
			}
		case "alias":
			aliasRows = append(aliasRows, fmt.Sprintf("%v@%v", r["target"], r["order"]))
		default:
			t.Fatalf("row %v has no kind", r)
		}
	}
	if !slices.Equal(aliasRows, []string{"openrouter/x/y@1", "omlx/qwen@2"}) {
		t.Fatalf("alias rows %v", aliasRows)
	}
}

// TestListJSONCarriesStore pins `wt-model litellm list --json` against the
// list block of docs/contracts/litellm-cli.sample.json: every row carries
// "store" and "kind", database rows (alias deployments too, #596) are listed
// beside config.yaml's, and "routed" names them too. The change is additive: the older keys keep their meaning, so a
// script reading "routed" or "rows[].managed" still works.
func TestListJSONCarriesStore(t *testing.T) {
	litellmEnv(t, listFixtureYAML)
	srv := listFixtureAPI(t)
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
		"general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n"+
		"router_settings:\n  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n")
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
		"general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n"+
		"router_settings:\n  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n")
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

// preStoreYAML is a config.yaml as a pre-#598 wt left it: a start sync that
// ran would add store_model_in_db to it (and owe a restart).
const preStoreYAML = "model_list: []\n" +
	"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n" +
	"general_settings:\n  cancel_on_disconnect: true\n" +
	"router_settings:\n  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n"

// TestServeStartSyncSkipsWhenItCannotSeeTheDatabase pins the start sync's
// gate (#598 review): at login serve listens before the proxy and its
// database answer, and a sync that cannot see the database would put every
// local route back in config.yaml and restart a proxy still starting. So a
// proxy that never answers, a database listing that fails and one that hangs
// each skip the sync with one warning: config.yaml byte for byte, no
// restart, and serve goes on serving.
func TestServeStartSyncSkipsWhenItCannotSeeTheDatabase(t *testing.T) {
	for name, set := range map[string]func(*fakeModelAPI){
		"proxy down": func(f *fakeModelAPI) { f.down = true },
		"list fails": func(f *fakeModelAPI) { f.failList = true },
		"list hangs": func(f *fakeModelAPI) { f.hangList = true },
	} {
		t.Run(name, func(t *testing.T) {
			fake, srv := newFakeModelAPI(t)
			set(fake)
			seedProxyHome(t, srv.URL, "sk-test", 0o600)
			p := litellmEnv(t, preStoreYAML)
			var bounces atomic.Int32
			done := make(chan struct{})
			rt := testRuntime(t)
			rt.StartSync = func(ctx context.Context, cfg *config.Config, errOut io.Writer) {
				defer close(done)
				startSync{
					probe:        func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} },
					waitReady:    litellm.WaitReady,
					bounce:       func(context.Context, io.Writer, *config.Config) { bounces.Add(1) },
					readyTimeout: 500 * time.Millisecond,
					listTimeout:  500 * time.Millisecond,
				}.run(ctx, cfg, errOut)
			}
			readErr, _, stop := serveUntilListeningWith(t, rt, writeToken(t, 0o600))
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = stop()
				t.Fatalf("start sync did not finish; stderr %q", readErr())
			}
			if n := strings.Count(readErr(), "wt-model: warning: LiteLLM routes not synced at start: "); n != 1 {
				t.Errorf("stderr %q, want one skip warning", readErr())
			}
			if strings.Contains(readErr(), "start route sync") {
				t.Errorf("stderr %q: the sync ran", readErr())
			}
			if b, _ := os.ReadFile(p); string(b) != preStoreYAML || bounces.Load() != 0 {
				t.Errorf("config.yaml %q bounces %d, want untouched and no restart", b, bounces.Load())
			}
			if err := stop(); err != nil {
				t.Fatalf("serve: %v", err)
			}
		})
	}
}

// TestStartSyncHandsItsRestartToTheTrackedBounce pins that the start sync
// never restarts the proxy itself (#598 review): its write is NoRestart, and
// a config.yaml it changed (here the first-time store_model_in_db) owes one
// restart, handed to lifecycle's tracked, one-at-a-time bounce. So the sync
// returns while that restart still runs (serve's stop does not wait on it
// through the start sync; it waits like for any route job's restart, through
// WaitPendingRoutes), and the restart cannot overlap a route job's.
func TestStartSyncHandsItsRestartToTheTrackedBounce(t *testing.T) {
	_, srv := newFakeModelAPI(t)
	seedProxyHome(t, srv.URL, "sk-test", 0o600)
	p := litellmEnv(t, preStoreYAML)
	t.Setenv("WT_LITELLM_RESTART_CMD", "sleep 2")
	t.Cleanup(lifecycle.WaitPendingRoutes)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var errOut lockedWriter
	began := time.Now()
	serveStartSync(func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} })(t.Context(), cfg, &errOut)
	if took := time.Since(began); took > time.Second || !lifecycle.RoutesPending() {
		t.Fatalf("start sync took %s, restart pending %v: want it returned with the restart still running", took, lifecycle.RoutesPending())
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "store_model_in_db: true") {
		t.Fatalf("config.yaml %q, want the setting written", b)
	}
	lifecycle.WaitPendingRoutes()
	if strings.Contains(errOut.String(), "sk-test") {
		t.Fatalf("stderr shows the key: %q", errOut.String())
	}
}

// TestStartSyncHonorsCancel pins that a serve stopped while the start sync
// is still waiting for the proxy (up to 90 s at login) is not held up by it:
// the wait ends with the serve's context, and nothing is written.
func TestStartSyncHonorsCancel(t *testing.T) {
	fake, srv := newFakeModelAPI(t)
	fake.down = true
	seedProxyHome(t, srv.URL, "sk-test", 0o600)
	p := litellmEnv(t, preStoreYAML)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	var errOut lockedWriter
	began := time.Now()
	serveStartSync(func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} })(ctx, cfg, &errOut)
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("start sync took %s after the cancel, want it to end with the context", took)
	}
	if b, _ := os.ReadFile(p); string(b) != preStoreYAML || !strings.Contains(errOut.String(), "not synced at start") {
		t.Fatalf("config.yaml %q stderr %q, want untouched and the skip warning", b, errOut.String())
	}
}

// lockedWriter is a strings.Builder safe for the restart's goroutine.
type lockedWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}
