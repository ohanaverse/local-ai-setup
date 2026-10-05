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
	listed     []string        // /v1/models
	pool       map[string]bool // every pool model -> loaded
	loading    string          // a pool model that is mid-load
	key        string          // required by /v1/models/status when set
	noHealth   bool            // an omlx old enough to have no /health
	statusHits int
}

func (f *fakeOmlx) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
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
		if f.key != "" && r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		ms := []map[string]any{}
		for id, l := range f.pool {
			ms = append(ms, map[string]any{"id": id, "loaded": l, "is_loading": id == f.loading})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
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
// picker. ServedIDs answers from what omlx says is loaded instead: /health's
// counts settle "none" and "all" with no key, and when they are mixed
// /v1/models/status names the loaded ones.
func TestServedIDsOmlxCountsOnlyLoadedModels(t *testing.T) {
	for _, tc := range []struct {
		name       string
		srv        fakeOmlx
		want       []string
		statusHits int
	}{
		{"up, nothing loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}}, nil, 0},
		{"every model loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": true}}, []string{"A", "B"}, 0},
		{"one of two loaded", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": true}}, []string{"B"}, 1},
		// A hidden model is in the pool's counts but not in the list, so "all
		// loaded" cannot be read off the list: only status says which.
		{"a hidden model loaded, the listed one not", fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false, "H": true}}, []string{"H"}, 1},
		// Every pool model loaded, but the list is one short: the list is not
		// the pool, so "all loaded" is answered by status, not by the list.
		{"all loaded, one of them hidden", fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true, "H": true}}, []string{"A", "H"}, 1},
		// health's loaded_count is the engines already built; status also
		// says which one is mid-load, and that one occupies the server.
		{"one loaded, one loading", fakeOmlx{listed: []string{"A", "B", "C"}, pool: map[string]bool{"A": true, "B": false, "C": false}, loading: "B"}, []string{"A", "B"}, 1},
		// An omlx with no /health predates the counts: the list is all wt has.
		{"no /health endpoint", fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": false, "B": false}, noHealth: true}, []string{"A", "B"}, 0},
	} {
		srv := tc.srv
		got, err := ServedIDs(omlxCfgAt(srv.serve(t), ""), testClient, "omlx")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) || srv.statusHits != tc.statusHits {
			t.Errorf("%s: served = %v (status asked %d times), want %v (%d)", tc.name, got, srv.statusHits, tc.want, tc.statusHits)
		}
	}
}

// TestServedIDsOmlxNeedsTheKeyOnlyForAMixedPool pins the key handling. omlx's
// status endpoint wants the server's API key when one is set (the list and
// /health do not), and wt has one only if the registry's omlx provider names
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
	// The key is not needed, and the status endpoint not asked, when the
	// counts already answer.
	idle := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, key: "sk-omlx"}
	if ids, err := ServedIDs(omlxCfgAt(idle.serve(t), ""), testClient, "omlx"); err != nil || len(ids) != 0 || idle.statusHits != 0 {
		t.Errorf("idle pool, no key: served = %v err = %v status asked %d times, want none, nil, 0", ids, err, idle.statusHits)
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
	mkdirs(t, dir, "A", "B")
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
