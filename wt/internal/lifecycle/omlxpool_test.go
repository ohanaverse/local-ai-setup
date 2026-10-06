package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// fakePool is an omlx 0.7.0 engine pool: health, list, status, load and unload
// by on-disk id. evictOnLoad names models omlx unloads when a load is asked
// for, whether or not the load then succeeds; loadCodes are answered in order
// before a load succeeds (507, 409...). The list names a model by its alias
// when it has one, as omlx does. statusFailsAfterLoad makes status answer 500
// once a load has been asked for, so the reading after a load is the fallback
// (/health and the list) while the one before it came from status.
//
// mgmtKey makes it an omlx with an API key that allows unauthenticated
// inference: status, load and unload want `Authorization: Bearer <mgmtKey>`
// and answer 401 without it (recorded in refused, and changing nothing), while
// /health, the list and chat completions stay keyless. A chat completion loads
// the model it names, as omlx does on a first request; chats records them.
type fakePool struct {
	mu                   sync.Mutex
	loaded               map[string]bool
	sizes                map[string]int64
	ceiling              int64
	alias                map[string]string
	statusFailsAfterLoad bool
	evictOnLoad          []string
	loadCodes            []int
	loads                []string
	unloads              []string
	mgmtKey              string
	refused              []string
	chats                []string
}

// refuse answers a management request 401 when the pool wants a key the
// request does not carry, recording it as what ("load B"). The caller holds mu.
func (f *fakePool) refuse(w http.ResponseWriter, r *http.Request, what string) bool {
	if f.mgmtKey == "" || r.Header.Get("Authorization") == "Bearer "+f.mgmtKey {
		return false
	}
	f.refused = append(f.refused, what)
	http.Error(w, `{"detail":"API key required"}`, http.StatusUnauthorized)
	return true
}

func (f *fakePool) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// handler is the pool's endpoints, for a test that binds its own listener.
func (f *fakePool) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, l := range f.loaded {
			if l {
				n++
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "engine_pool": map[string]int{"model_count": len(f.loaded), "loaded_count": n}})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		data := []map[string]string{}
		for id := range f.loaded {
			if a := f.alias[id]; a != "" {
				id = a
			}
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("GET /v1/models/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.refuse(w, r, "status") {
			return
		}
		if f.statusFailsAfterLoad && len(f.loads) > 0 {
			http.Error(w, `{"detail":"Internal Server Error"}`, http.StatusInternalServerError)
			return
		}
		ms := []map[string]any{}
		var inUse int64
		for id, l := range f.loaded {
			if l {
				inUse += f.sizes[id]
			}
			ms = append(ms, map[string]any{"id": id, "loaded": l, "resident_estimated_size": f.sizes[id]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"final_ceiling": f.ceiling, "current_model_memory": inUse, "models": ms})
	})
	mux.HandleFunc("POST /v1/models/{id}/load", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		if f.refuse(w, r, "load "+id) {
			return
		}
		f.loads = append(f.loads, id)
		for _, v := range f.evictOnLoad {
			f.loaded[v] = false
		}
		if len(f.loadCodes) > 0 {
			code := f.loadCodes[0]
			f.loadCodes = f.loadCodes[1:]
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":"Cannot load: would exceed the memory ceiling. unload another model."}`))
			return
		}
		if _, ok := f.loaded[id]; !ok {
			http.Error(w, `{"detail":"Model not found"}`, http.StatusNotFound)
			return
		}
		f.loaded[id] = true
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v1/models/{id}/unload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		if f.refuse(w, r, "unload "+id) {
			return
		}
		f.unloads = append(f.unloads, id)
		if !f.loaded[id] {
			http.Error(w, `{"detail":"Model not loaded"}`, http.StatusBadRequest)
			return
		}
		f.loaded[id] = false
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.chats = append(f.chats, body.Model)
		if _, ok := f.loaded[body.Model]; !ok {
			http.Error(w, `{"detail":"Model not found"}`, http.StatusNotFound)
			return
		}
		f.loaded[body.Model] = true
		_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
	})
	return mux
}

func (f *fakePool) isLoaded(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loaded[id]
}

// poolEnv is a testEnv whose omlx backend is the real one, whose inventory is
// snap, and which records the occupant hook and fails the test on any CLI run
// (a pool start or model stop must never run `omlx stop`).
func poolEnv(t *testing.T, snap localmodels.Snapshot) (*env, *[]string) {
	t.Helper()
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ran %s %v: a pool start or model stop must not run the omlx CLI", name, args)
		return nil, nil
	}
	var stopped []string
	e.onOccupantStopped = func(_ context.Context, _ *config.Config, occ localmodels.Entry) bool {
		stopped = append(stopped, occ.ModelID)
		return true
	}
	return e, &stopped
}

func poolSnap(ceiling int64, sizes map[string]int64, loaded ...string) localmodels.Snapshot {
	p := &localmodels.Pool{Ceiling: ceiling, SizesKnown: true}
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}, OmlxPool: p}
	for id, size := range sizes {
		l := slices.Contains(loaded, id)
		p.Models = append(p.Models, localmodels.PoolModel{ID: id, Loaded: l, Size: size})
		if l {
			p.InUse += size
		}
		snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/" + id, ModelName: id, Artifact: id, Running: l})
	}
	return snap
}

// captureRoutes redirects route output for one test.
func captureRoutes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := routesWarn
	routesWarn = &buf
	t.Cleanup(func() { routesWarn = old })
	return &buf
}

// TestStartOnPoolLoadsBesideALoadedModel pins #213 item 1: starting a second
// omlx model that fits loads it and leaves the first one loaded. It used to
// run `omlx stop`, unloading a model another session was using.
func TestStartOnPoolLoadsBesideALoadedModel(t *testing.T) {
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}, Options{})
	if err != nil {
		t.Fatalf("start = %v, want nil with no replace needed", err)
	}
	if !reflect.DeepEqual(fp.loads, []string{"B"}) || !fp.isLoaded("A") || len(*stopped) != 0 {
		t.Errorf("loads = %v, A loaded = %v, occupant hook = %v; want [B], true, none", fp.loads, fp.isLoaded("A"), *stopped)
	}
}

// TestStartOnPoolAsksBeforeEvicting verifies a start that does not fit is
// refused with the models it would unload, and sends no load: a load is what
// makes omlx evict, so asking after it would be too late.
func TestStartOnPoolAsksBeforeEvicting(t *testing.T) {
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100}
	e, _ := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || !reflect.DeepEqual(occ.IDs(), []string{"omlx/A"}) {
		t.Fatalf("start = %v, want *OccupiedError naming omlx/A", err)
	}
	if len(fp.loads) != 0 {
		t.Errorf("loads = %v, want none before the user agrees", fp.loads)
	}
}

// TestStartOnPoolRemovesAnEvictedModelsRoute verifies that once the user
// agrees, omlx does the evicting and wt drops the evicted model's route and
// tells the caller, so no route points at a model that is no longer loaded.
func TestStartOnPoolRemovesAnEvictedModelsRoute(t *testing.T) {
	out := captureRoutes(t)
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	var unloaded []string
	opts := Options{AllowReplace: true, OnUnloaded: func(en localmodels.Entry) { unloaded = append(unloaded, en.ModelID) }}
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) || !reflect.DeepEqual(unloaded, []string{"omlx/A"}) || !e.restartOwed {
		t.Errorf("hook = %v, OnUnloaded = %v, restartOwed = %v; want [omlx/A] twice and true", *stopped, unloaded, e.restartOwed)
	}
	if got := out.String(); !containsFold(got, "omlx unloaded omlx/A") || containsFold(got, "not predicted") {
		t.Errorf("output = %q, want the eviction line without 'not predicted'", got)
	}
}

// TestStartOnPoolReportsAnUnpredictedEviction covers omlx's soft watermark: a
// load wt predicted would fit can still evict. The route must go and the line
// must say wt did not see it coming, or a session loses its model silently.
func TestStartOnPoolReportsAnUnpredictedEviction(t *testing.T) {
	out := captureRoutes(t)
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) || !containsFold(out.String(), "omlx unloaded omlx/A to make room (not predicted)") {
		t.Errorf("hook = %v, output = %q", *stopped, out.String())
	}
}

// TestStartOnPoolKeepsRoutesWhenTheReadingAfterTheLoadIsTheFallback pins the
// final-review finding F1. The reading before the load came from status; the
// one after it comes from /health and the list, because status failed once.
// The list names an aliased model by its alias, so the still-loaded sibling is
// not found in it by directory name. Reading that as "omlx unloaded it" removed
// the route of a model that was still serving, restarted the proxy, and told
// modelman to clear its running flag: every session on it got "Invalid model
// name". A fallback reading names too little to prove an eviction, so it must
// change nothing.
func TestStartOnPoolKeepsRoutesWhenTheReadingAfterTheLoadIsTheFallback(t *testing.T) {
	out := captureRoutes(t)
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{
		loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100,
		alias: map[string]string{"A": "my-alias"}, statusFailsAfterLoad: true,
	}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	cfg := provCfg("omlx", fp.serve(t))
	var unloaded []string
	opts := Options{OnUnloaded: func(en localmodels.Entry) { unloaded = append(unloaded, en.ModelID) }}
	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}, opts); err != nil {
		t.Fatalf("start = %v, want nil: B fits beside A", err)
	}
	// The premise: the reading reconcilePool gets is the fallback, and it does
	// not name A, though A is loaded.
	pool, err := localmodels.OmlxPool(cfg, e.probeClient)
	if _, found := pool.Find("A"); err != nil || pool.SizesKnown || found || !fp.isLoaded("A") || !fp.isLoaded("B") {
		t.Fatalf("pool after the load = %+v err = %v, A loaded = %v, B loaded = %v; want a fallback reading without A while both are loaded", pool, err, fp.isLoaded("A"), fp.isLoaded("B"))
	}
	if len(*stopped) != 0 || len(unloaded) != 0 || e.restartOwed {
		t.Errorf("occupant hook = %v, OnUnloaded = %v, restartOwed = %v; want none, none, false: A is still loaded", *stopped, unloaded, e.restartOwed)
	}
	if got := out.String(); containsFold(got, "omlx unloaded") {
		t.Errorf("output = %q, want no eviction line for a model that is still loaded", got)
	}
}

// TestResolveEvictionsOnPoolReprobeNamesEveryLoadedModel verifies that when
// the snapshot's omlx probe cannot be trusted, the re-probe names every other
// model omlx has loaded — not just the first, which is all an exclusive server
// can have. The re-probe has no sizes, so any of them may be unloaded; a
// prompt naming one of two would get a "yes" that the second session never
// agreed to.
func TestResolveEvictionsOnPoolReprobeNamesEveryLoadedModel(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": true, "T": false}}
	e := testEnv()
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}
	victims, unknown := e.resolveEvictions(context.Background(), provCfg("omlx", fp.serve(t)), "omlx", Target{ProviderID: "omlx", ModelName: "T"}, snap)
	got := ids(victims)
	slices.Sort(got)
	if unknown || !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("victims = %v, unknown = %v; want both loaded models and a known answer", got, unknown)
	}
}

// TestStartOnPoolFailedLoadStillReconciles verifies a load omlx refuses for
// want of memory is reported as that, and that a model omlx evicted on the way
// to refusing still loses its route.
func TestStartOnPoolFailedLoadStillReconciles(t *testing.T) {
	captureRoutes(t)
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}, loadCodes: []int{http.StatusInsufficientStorage}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{AllowReplace: true})
	var noRoom *NoRoomError
	if !errors.As(err, &noRoom) || !containsFold(noRoom.Error(), "memory ceiling") {
		t.Fatalf("start = %v, want *NoRoomError carrying omlx's message", err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) {
		t.Errorf("hook = %v, want the evicted omlx/A even though the load failed", *stopped)
	}
}

// TestOmlxLoadWaitsOutABusyAnswer verifies a 409 (the model is already
// loading) is waited for instead of failing: a second `wt start` of a model
// mid-load must end with it loaded, not with an error.
func TestOmlxLoadWaitsOutABusyAnswer(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"B": false}, loadCodes: []int{http.StatusConflict, http.StatusConflict}}
	if err := omlxLoad(context.Background(), testEnv(), provCfg("omlx", fp.serve(t)), "org/B"); err != nil {
		t.Fatal(err)
	}
	if len(fp.loads) != 3 || !fp.isLoaded("B") {
		t.Errorf("loads = %v, B loaded = %v; want three attempts by on-disk id and loaded", fp.loads, fp.isLoaded("B"))
	}
}

// TestOmlxLoadFailsFastWhenOmlxGoesAway verifies a load whose connection is
// refused ends at once, saying omlx stopped answering. omlxLoad runs only
// after the server has been seen answering, so a refusal there means omlx went
// away — most likely it ran out of memory loading the model. Retrying it for
// the whole warmup budget left the user watching "warming the model" for ten
// minutes over a server that was already gone.
func TestOmlxLoadFailsFastWhenOmlxGoesAway(t *testing.T) {
	srv := httptest.NewServer((&fakePool{loaded: map[string]bool{"B": false}}).handler())
	url := srv.URL
	srv.Close()
	e := testEnv()
	e.warmupTimeout = 5 * time.Second
	began := time.Now()
	err := omlxLoad(context.Background(), e, provCfg("omlx", url), "B")
	if took := time.Since(began); took > time.Second {
		t.Errorf("load took %v against a refused connection, want it to fail at once (budget %v)", took, e.warmupTimeout)
	}
	if err == nil || !errors.Is(err, syscall.ECONNREFUSED) || !containsFold(err.Error(), "omlx stopped answering while loading B") {
		t.Errorf("err = %v, want it to say omlx stopped answering while loading B", err)
	}
}

// TestStopModelOnPoolUnloadsOnlyThatModel pins #213 item 1's other half:
// stopping one omlx model unloads it and leaves the service and its siblings
// up. It used to run `omlx stop`, taking every loaded model down.
func TestStopModelOnPoolUnloadsOnlyThatModel(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": true}}
	e, _ := poolEnv(t, localmodels.Snapshot{})
	if err := stopModel(context.Background(), e, provCfg("omlx", fp.serve(t)), "omlx", "A"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fp.unloads, []string{"A"}) || fp.isLoaded("A") || !fp.isLoaded("B") {
		t.Errorf("unloads = %v, A = %v, B = %v", fp.unloads, fp.isLoaded("A"), fp.isLoaded("B"))
	}
	// Already gone is success: the goal is "not loaded".
	if err := stopModel(context.Background(), e, provCfg("omlx", fp.serve(t)), "omlx", "A"); err != nil {
		t.Errorf("stopping an unloaded model = %v, want nil", err)
	}
}

// TestPoolRouteChanges pins the route rules for a pool: a start adds its model
// and clears nothing else, and a model stop removes that model's ids only.
// Clearing the family is what made sync and the start hook undo each other
// with two models loaded (#213 item 2).
func TestPoolRouteChanges(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/reg", ProviderID: "omlx", ModelName: "org/Reg-4bit", Location: config.LocationLocal}},
	}
	if ch := StartRouteChange(cfg, Target{ProviderID: "omlx", ModelName: "org/Reg-4bit", ModelID: "omlx/reg"}); len(ch.RemoveFamilies) != 0 {
		t.Errorf("start removes families %v, want none on a pool", ch.RemoveFamilies)
	}
	got := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/reg", ModelName: "org/Reg-4bit"})
	if !slices.Contains(got, "omlx/reg") {
		t.Errorf("route ids = %v, want the registry id", got)
	}
	disc := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelID: "Loose-4bit", ModelName: "Loose-4bit"})
	if !reflect.DeepEqual(disc, []string{"omlx/Loose-4bit"}) {
		t.Errorf("route ids for an entry the re-probe built = %v, want its discovered id only", disc)
	}
	// omlx and omlx-6bit are one server: a registry row under each provider
	// id can name the same directory, and both are routed while it is loaded.
	// A model stop passes one provider id; consulting only that one left the
	// sibling row's route pointing at a model that had just been unloaded.
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "omlx-6bit/reg", ProviderID: "omlx-6bit", ModelName: "Reg-4bit", Location: config.LocationLocal})
	both := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelName: "org/Reg-4bit"})
	if !reflect.DeepEqual(both, []string{"omlx/reg", "omlx-6bit/reg"}) {
		t.Errorf("route ids with a sibling provider's row for the same model = %v, want [omlx/reg omlx-6bit/reg]", both)
	}
	if disc := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelName: "Loose-4bit"}); !reflect.DeepEqual(disc, []string{"omlx/Loose-4bit"}) {
		t.Errorf("route ids with no registry row = %v, want the discovered id as the fallback", disc)
	}
}

// TestStopModelDeferredOnPoolKeepsSiblingRoutes drives the public stop through
// the real omlx backend and the real litellm.ApplyChange: stopping one omlx
// model unloads it and removes its row only. The family sweep an Exclusive
// stop does would here delete the routes of models that are still loaded, and
// every session on them would get "Invalid model name" from the proxy (#213).
func TestStopModelDeferredOnPoolKeepsSiblingRoutes(t *testing.T) {
	const rows = `model_list:
  - model_name: omlx/a
    litellm_params: {model: openai/A, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx/B
    litellm_params: {model: openai/B, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
`
	path, _, warn := realRoutes(t, rows)
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": true, "Six": true}}
	url := fp.serve(t)
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: url}},
			{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: url}},
		},
		Models: []config.Model{
			{ID: "omlx/a", ProviderID: "omlx", ModelName: "org/A", Location: config.LocationLocal},
			{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		},
	}
	owed, err := StopModelDeferred(context.Background(), cfg, "omlx", "org/A")
	if err != nil || !owed {
		t.Fatalf("StopModelDeferred = (%v, %v), want (true, nil)", owed, err)
	}
	SettleRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"omlx/B", "omlx-6bit/Six"}) {
		t.Errorf("routed = %v, want the stopped model's row gone and both loaded siblings kept (warn %q)", got, warn.String())
	}
	if !reflect.DeepEqual(fp.unloads, []string{"A"}) || !fp.isLoaded("B") || !fp.isLoaded("Six") {
		t.Errorf("unloads = %v, B = %v, Six = %v; want only A unloaded", fp.unloads, fp.isLoaded("B"), fp.isLoaded("Six"))
	}
}

// keylessSnap is the inventory of an omlx whose status wants a key the
// registry does not name: the probe is trusted, the reading is the fallback
// (no sizes), and loaded are the models it found loaded.
func keylessSnap(loaded ...string) localmodels.Snapshot {
	p := &localmodels.Pool{}
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}, OmlxPool: p}
	for _, id := range loaded {
		p.Models = append(p.Models, localmodels.PoolModel{ID: id, Loaded: true})
		snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/" + id, ModelName: id, Artifact: id, Running: true})
	}
	return snap
}

// TestStartOnPoolWithoutAKeyFallsBackToTheChatWarmup pins the regression the
// #213 live check found. The common local setup is an omlx with an API key
// that allows unauthenticated inference, and a registry with no secret_ref:
// omlx refuses the keyless /load (401) but serves a keyless chat completion,
// and loads the model on it. `main` starts a model there through that chat
// warmup; a start that stops at the refused /load made `wt start` fail on
// every such machine.
func TestStartOnPoolWithoutAKeyFallsBackToTheChatWarmup(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"B": false}, mgmtKey: "sk-omlx"}
	e, stopped := poolEnv(t, keylessSnap())
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "org/B", ModelID: "omlx/b"}, Options{})
	if err != nil {
		t.Fatalf("start = %v, want nil through the chat warmup", err)
	}
	var refusedLoads []string
	for _, r := range fp.refused {
		if r != "status" {
			refusedLoads = append(refusedLoads, r)
		}
	}
	if !reflect.DeepEqual(refusedLoads, []string{"load B"}) || len(fp.loads) != 0 {
		t.Errorf("refused = %v, accepted loads = %v; want one refused load of B and none accepted", fp.refused, fp.loads)
	}
	if !reflect.DeepEqual(fp.chats, []string{"B"}) || !fp.isLoaded("B") || len(*stopped) != 0 {
		t.Errorf("chats = %v, B loaded = %v, occupant hook = %v; want one chat naming the directory basename, loaded, none", fp.chats, fp.isLoaded("B"), *stopped)
	}
}

// TestStartOnPoolWithAWrongKeyDoesNotFallBack verifies the fallback is for a
// registry that names no key only. When the registry names one and omlx
// refuses it, the key is wrong and the user has to fix it: warming keyless
// instead would start the model and hide that wt can neither predict evictions
// nor unload on that server.
func TestStartOnPoolWithAWrongKeyDoesNotFallBack(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"B": false}, mgmtKey: "sk-omlx"}
	e, _ := poolEnv(t, keylessSnap())
	err := start(context.Background(), e, keyedProvCfg(fp.serve(t), "sk-stale"), Target{ProviderID: "omlx", ModelName: "org/B"}, Options{})
	var kr *KeyRefusedError
	if !errors.As(err, &kr) || !kr.KeySent {
		t.Fatalf("start = %v, want *KeyRefusedError with KeySent", err)
	}
	if len(fp.chats) != 0 || fp.isLoaded("B") {
		t.Errorf("chats = %v, B loaded = %v; want no chat request and nothing loaded", fp.chats, fp.isLoaded("B"))
	}
}

// TestStopModelOnPoolWithoutAKeySaysHowToProceed verifies a model stop omlx
// refuses for want of a key fails with both ways out — name the key so wt can
// unload one model, or stop the whole service — and does neither itself. It
// must never fall back to `omlx stop`: that takes down every other loaded
// model, which a model stop promises not to do.
func TestStopModelOnPoolWithoutAKeySaysHowToProceed(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": true}, mgmtKey: "sk-omlx"}
	e, _ := poolEnv(t, localmodels.Snapshot{})
	err := stopModel(context.Background(), e, provCfg("omlx", fp.serve(t)), "omlx", "A")
	var kr *KeyRefusedError
	if !errors.As(err, &kr) || kr.KeySent {
		t.Fatalf("stopModel = %v, want an error wrapping *KeyRefusedError with no key sent", err)
	}
	if msg := err.Error(); strings.Count(msg, "secret_ref") != 1 || strings.Count(msg, "wt stop omlx") != 1 {
		t.Errorf("message = %q, want it to name auth.secret_ref and `wt stop omlx` once each", msg)
	}
	if !fp.isLoaded("A") || !fp.isLoaded("B") || len(fp.unloads) != 0 {
		t.Errorf("A = %v, B = %v, accepted unloads = %v; want both still loaded", fp.isLoaded("A"), fp.isLoaded("B"), fp.unloads)
	}
}
