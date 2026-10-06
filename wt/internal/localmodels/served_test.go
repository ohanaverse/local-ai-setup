package localmodels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// fakeOmlx is an omlx server as 0.7.0 answers: /v1/models lists every model of
// the pool it does not hide, loaded or not; /health gives the pool's counts
// with no key; /v1/models/status names the loaded ones but is management-auth,
// so with key set it wants that Bearer token.
type fakeOmlx struct {
	listed []string        // /v1/models
	pool   map[string]bool // every pool model -> loaded
	// loading is a pool model that is mid-load; healthStatus is /health's
	// status code (0 = 200; omlx answers 503 while pinned models preload).
	loading      string
	healthStatus int
	key          string // required by /v1/models and /v1/models/status when set
	noHealth     bool   // an omlx old enough to have no /health
	statusHits   int
	noStatus     bool             // an omlx old enough to have no status endpoint
	sizes        map[string]int64 // resident_estimated_size per model
	estimated    map[string]int64 // estimated_size per model
	pinned       map[string]bool
	lastAccess   map[string]float64
	ceiling      int64 // final_ceiling
	inUse        int64 // current_model_memory
}

func (f *fakeOmlx) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if f.key != "" && r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		data := []map[string]string{}
		for _, id := range f.listed {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if f.noHealth {
			http.NotFound(w, r)
			return
		}
		if f.healthStatus != 0 && f.healthStatus != http.StatusOK {
			w.WriteHeader(f.healthStatus)
		}
		loaded := 0
		for _, l := range f.pool {
			if l {
				loaded++
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "engine_pool": map[string]int{"model_count": len(f.pool), "loaded_count": loaded}})
	})
	mux.HandleFunc("/v1/models/status", func(w http.ResponseWriter, r *http.Request) {
		f.statusHits++
		if f.noStatus {
			http.NotFound(w, r)
			return
		}
		if f.key != "" && r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		ms := []map[string]any{}
		for id, l := range f.pool {
			ms = append(ms, map[string]any{
				"id": id, "loaded": l, "is_loading": id == f.loading,
				"pinned": f.pinned[id], "resident_estimated_size": f.sizes[id],
				"estimated_size": f.estimated[id],
				"last_access":    f.lastAccess[id],
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"final_ceiling": f.ceiling, "current_model_memory": f.inUse, "models": ms,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func omlxCfgAt(url, secretRef string) *config.Config {
	p := config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: url}}
	if secretRef != "" {
		p.Auth = config.AuthConfig{Type: "api_key", BaseURL: url, SecretRef: secretRef}
	}
	return &config.Config{Providers: []config.Provider{p}}
}

// TestServedIDsOmlxCountsOnlyLoadedModels pins #201. omlx's /v1/models lists
// every model in its pool, loaded or not, and wt read that list as "running":
// with the service up and nothing loaded, every omlx model on disk showed as
// running, was a launch row, was routed by sync and offered by the stop
// picker. ServedIDs answers from what omlx says is loaded: status when it answers,
// else /health's counts and the list.
func TestServedIDsOmlxCountsOnlyLoadedModels(t *testing.T) {
	for _, tc := range []struct {
		name string
		srv  fakeOmlx
		want []string
	}{
		{"up, nothing loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}}, nil},
		{"every model loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": true}}, []string{"A", "B"}},
		{"one of two loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": true}}, []string{"B"}},
		// A hidden model is in the pool's counts but not in the list, so "all
		// loaded" cannot be read off the list: only status says which.
		{"a hidden model loaded, the listed one not", fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false, "H": true}}, []string{"H"}},
		// Every pool model loaded, but the list is one short: the list is not
		// the pool, so "all loaded" is answered by status, not by the list.
		{"all loaded, one of them hidden", fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true, "H": true}}, []string{"A", "H"}},
		// health's loaded_count is the engines already built; status also
		// says which one is mid-load, and that one occupies the server.
		{"one loaded, one loading", fakeOmlx{listed: []string{"A", "B", "C"}, pool: map[string]bool{"A": true, "B": false, "C": false}, loading: "B"}, []string{"A", "B"}},
		// An omlx with no /health predates the counts: the list is all wt has.
		{"no /health endpoint", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}, noHealth: true, noStatus: true}, []string{"A", "B"}},
		// /health 503s while pinned models preload: its counts say nothing is
		// built, but loaded_count cannot see a model mid-load, so zero built
		// does not settle "nothing" until status has said no one is loading.
		{"503 while preloading, status says none loading", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}, healthStatus: http.StatusServiceUnavailable}, nil},
		{"503 while preloading, one model mid-load", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}, loading: "B", healthStatus: http.StatusServiceUnavailable}, []string{"B"}},
	} {
		srv := tc.srv
		got, err := ServedIDs(omlxCfgAt(srv.serve(t), ""), testClient, "omlx")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: served = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestServedIDsOmlxNeedsTheKeyOnlyForAMixedPool pins the key handling. omlx's
// status endpoint wants the server's API key when one is set (as its list
// does; /health does not), and wt has one only if the registry's omlx provider names
// a secret_ref. Without it a mixed pool is an error — the caller's "running
// state is not trustworthy" — never a guess: reporting nothing loaded would
// let a start replace a serving model unasked, and reporting everything
// loaded is the bug. With the secret_ref the key is sent and the answer read.
func TestServedIDsOmlxNeedsTheKeyOnlyForAMixedPool(t *testing.T) {
	mixed := func() *fakeOmlx {
		return &fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": true}, key: "sk-omlx"}
	}
	srv := mixed()
	if ids, err := ServedIDs(omlxCfgAt(srv.serve(t), ""), testClient, "omlx"); err == nil {
		t.Errorf("no key: served = %v with no error, want an error for a mixed pool whose status is refused", ids)
	} else if refused(err) {
		t.Errorf("no key: err = %v reads as a refused connection; the server is up", err)
	}
	t.Setenv("WT_TEST_OMLX_KEY", "sk-omlx")
	srv = mixed()
	ids, err := ServedIDs(omlxCfgAt(srv.serve(t), "os.environ/WT_TEST_OMLX_KEY"), testClient, "omlx")
	if err != nil || !slices.Equal(ids, []string{"B"}) {
		t.Errorf("with the registry's secret_ref: served = %v err = %v, want [B]", ids, err)
	}
	// A refused status does not make an idle pool an error: /health settles it.
	idle := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, key: "sk-omlx"}
	if ids, err := ServedIDs(omlxCfgAt(idle.serve(t), ""), testClient, "omlx"); err != nil || len(ids) != 0 {
		t.Errorf("idle pool, no key: served = %v err = %v want none, nil", ids, err)
	}
}

// TestServedIDsOmlxFullyLoadedKeyedPool pins what a real keyed omlx (0.7.0)
// showed: with every pool model loaded wt reads /v1/models, which such a
// server refuses without the key. That refusal was returned as the answer —
// "no usable answer" for the commonest keyed state, a one-model pool with its
// model loaded — where status, asked with the key, says exactly what is
// loaded.
func TestServedIDsOmlxFullyLoadedKeyedPool(t *testing.T) {
	t.Setenv("WT_TEST_OMLX_KEY", "sk-omlx")
	srv := &fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": true}, key: "sk-omlx"}
	ids, err := ServedIDs(omlxCfgAt(srv.serve(t), "os.environ/WT_TEST_OMLX_KEY"), testClient, "omlx")
	slices.Sort(ids)
	if err != nil || !slices.Equal(ids, []string{"A", "B"}) {
		t.Errorf("keyed, all loaded: served = %v err = %v, want [A B]", ids, err)
	}
	// Without the key nothing can say, and that is still an error.
	srv = &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, key: "sk-omlx"}
	if ids, err := ServedIDs(omlxCfgAt(srv.serve(t), ""), testClient, "omlx"); err == nil {
		t.Errorf("keyed, all loaded, no key: served = %v with no error, want an error", ids)
	}
}

// TestServedIDsOtherFamiliesReadTheList pins that only omlx changed: mtplx and
// mlx_lm_server serve one model per process, so their /v1/models is already
// what is loaded, and a server that is down is still a refused connection.
func TestServedIDsOtherFamiliesReadTheList(t *testing.T) {
	srv := modelsServer(t, "Org/Q")
	cfg := &config.Config{Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: srv.URL}}}}
	if ids, err := ServedIDs(cfg, testClient, "mtplx"); err != nil || !slices.Equal(ids, []string{"Org/Q"}) {
		t.Errorf("mtplx served = %v err = %v, want [Org/Q]", ids, err)
	}
	if _, err := ServedIDs(omlxCfgAt("http://127.0.0.1:1", ""), testClient, "omlx"); !refused(err) {
		t.Errorf("omlx down: err = %v, want a refused connection", err)
	}
}

// TestInventoryOmlxListedButUnloadedIsNotRunning pins #201 where users see it:
// the inventory entry of an omlx model that is on disk and listed by a running
// service, but not loaded, is not Running — so the picker offers to start it
// and sync does not route it — while the family stays trusted (StatusOK). A
// mixed pool wt cannot read is StatusPartial, the existing "Running is not
// trustworthy" state, and is not Down.
func TestInventoryOmlxListedButUnloadedIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "A", "B")
	models := []config.Model{{ID: "omlx/A", ProviderID: "omlx", ModelName: "A"}, {ID: "omlx/B", ProviderID: "omlx", ModelName: "B"}}
	run := func(f *fakeOmlx) Snapshot {
		return inventory(&config.Config{Providers: []config.Provider{localProvider("omlx", f.serve(t), dir)}, Models: models}, testClient)
	}
	snap := run(&fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}})
	for _, id := range []string{"omlx/A", "omlx/B"} {
		if en, ok := byModelID(snap, id); !ok || en.Running || !en.ArtifactKnown {
			t.Errorf("idle service: %s = %+v ok=%v, want on disk and not running", id, en, ok)
		}
	}
	if got := snap.Providers["omlx"]; got != StatusOK {
		t.Errorf("idle service: status = %q, want %q", got, StatusOK)
	}
	snap = run(&fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": true}})
	a, _ := byModelID(snap, "omlx/A")
	b, _ := byModelID(snap, "omlx/B")
	if a.Running || !b.Running {
		t.Errorf("one loaded: A.Running=%v B.Running=%v, want false/true", a.Running, b.Running)
	}
	snap = run(&fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": true}, key: "sk"})
	if got := snap.Providers["omlx"]; got != StatusPartial || snap.Down["omlx"] {
		t.Errorf("mixed pool, status refused: status = %q down = %v, want %q and not down", got, snap.Down["omlx"], StatusPartial)
	}
}
