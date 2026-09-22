package localmodels

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

var testClient = &http.Client{Timeout: 2 * time.Second}

// roundTripFunc adapts a function to http.RoundTripper, so a test can answer
// one path with a real transport error (a refused connection) while leaving
// the rest of the request going to the live stub server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// modelsServer serves an OpenAI-compatible /v1/models body.
func modelsServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]string{}
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ollamaServer serves /api/tags (pulled) and /api/ps (loaded).
func ollamaServer(t *testing.T, pulled, loaded []string) *httptest.Server {
	t.Helper()
	body := func(names []string) map[string]any {
		ms := []map[string]string{}
		for _, n := range names {
			ms = append(ms, map[string]string{"name": n})
		}
		return map[string]any{"models": ms}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body(pulled)) })
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body(loaded)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func localProvider(id, baseURL, modelDir string) config.Provider {
	return config.Provider{ID: id, Location: config.LocationLocal, ModelDir: modelDir, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.Mkdir(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func byModelID(s Snapshot, id string) (Entry, bool) {
	for _, e := range s.Entries {
		if e.ModelID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// TestInventoryOllamaDiscoveredAndRunning verifies pulled-but-unregistered
// ollama models are listed with DiscoveredModelID ids, and that RUNNING comes
// from /api/ps (loaded set) — not from anything modelman flags.
func TestInventoryOllamaDiscoveredAndRunning(t *testing.T) {
	srv := ollamaServer(t, []string{"gemma4:9b", "qwen3:8b"}, []string{"qwen3:8b"})
	cfg := &config.Config{Providers: []config.Provider{localProvider("ollama", srv.URL, "")}}
	snap := inventory(cfg, testClient)

	g, ok := byModelID(snap, config.DiscoveredModelID("ollama", "gemma4:9b"))
	if !ok || g.Registered || g.Running || g.Artifact != "gemma4:9b" {
		t.Errorf("gemma4 = %+v ok=%v, want discovered, not running", g, ok)
	}
	q, ok := byModelID(snap, config.DiscoveredModelID("ollama", "qwen3:8b"))
	if !ok || !q.Running {
		t.Errorf("qwen3 = %+v ok=%v, want running", q, ok)
	}
	if snap.Providers["ollama"] != StatusOK {
		t.Errorf("status = %q", snap.Providers["ollama"])
	}
}

// TestInventoryDefaultModelDirViaExportedInventory verifies an omlx provider
// with no model_dir falls back to ~/.omlx/models (via config.ExpandHome) and
// that the exported Inventory wrapper reports the on-disk model as running when
// the server serves it. The live registry's omlx row has no model_dir, so
// production always takes this default path.
func TestInventoryDefaultModelDirViaExportedInventory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	root := filepath.Join(tmp, ".omlx", "models")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "Qwen3.8-27B-4bit")
	srv := modelsServer(t, "Qwen3.8-27B-4bit")
	cfg := &config.Config{Providers: []config.Provider{localProvider("omlx", srv.URL, "")}}

	snap := Inventory(cfg)
	e, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Qwen3.8-27B-4bit"))
	if !ok || !e.Running {
		t.Errorf("entry = %+v ok=%v, want discovered and running; snapshot=%+v", e, ok, snap)
	}
}

// TestInventoryRegisteredMatchKeepsRegistryID verifies an on-disk oMLX dir that
// matches a registry model (dir "Qwen3.8-27B-4bit" vs model_name
// "mlx-community/Qwen3.8-27B-4bit") is reported under the REGISTRY id and marked
// registered, while a sibling 8-bit dir with no registry entry is discovered —
// so usage/survey history stays keyed on the registry id (sub-project 1).
func TestInventoryRegisteredMatchKeepsRegistryID(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Qwen3.8-27B-4bit", "Qwen3.8-27B-8bit")
	srv := modelsServer(t, "Qwen3.8-27B-4bit")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", srv.URL, root)},
		Models:    []config.Model{{ID: "omlx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "omlx", ModelName: "mlx-community/Qwen3.8-27B-4bit"}},
	}
	snap := inventory(cfg, testClient)

	reg, ok := byModelID(snap, "omlx/mlx-community--Qwen3.8-27B-4bit")
	if !ok || !reg.Registered || reg.Artifact != "Qwen3.8-27B-4bit" || !reg.Running {
		t.Errorf("registered = %+v ok=%v", reg, ok)
	}
	disc, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Qwen3.8-27B-8bit"))
	if !ok || disc.Registered || disc.Running {
		t.Errorf("discovered 8bit = %+v ok=%v, want unregistered, not running", disc, ok)
	}
	if len(snap.Entries) != 2 {
		t.Errorf("entries = %d, want 2 (4bit must not also appear as discovered)", len(snap.Entries))
	}
}

// TestInventoryMtplxRepoIDMapping verifies an mtplx dir "Org--Model" is listed
// as artifact/id "Org/Model" and reads running when /v1/models serves that repo
// id — matching how the registry spells mtplx models.
func TestInventoryMtplxRepoIDMapping(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality")
	srv := modelsServer(t, "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
	cfg := &config.Config{Providers: []config.Provider{localProvider("mtplx", srv.URL+"/v1", root)}}
	snap := inventory(cfg, testClient)

	e, ok := byModelID(snap, "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
	if !ok || e.Artifact != "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" || !e.Running {
		t.Errorf("entry = %+v ok=%v", e, ok)
	}
}

// TestInventoryRegisteredMissingFromDisk verifies a configured local model with
// no matching artifact still appears (registered, not running, empty Artifact)
// so the selector can show a configured model that is missing from disk.
func TestInventoryRegisteredMissingFromDisk(t *testing.T) {
	srv := ollamaServer(t, []string{"other:1b"}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/gone:7b", ProviderID: "ollama", ModelName: "gone:7b"}},
	}
	e, ok := byModelID(inventory(cfg, testClient), "ollama/gone:7b")
	if !ok || !e.Registered || e.Running || e.Artifact != "" {
		t.Errorf("entry = %+v ok=%v", e, ok)
	}
}

// TestInventoryOllamaLatestFallback verifies a tagless registry name finds the
// pulled ":latest" model (ollama resolves it that way), so it is not listed
// twice — once registered and once as a discovered duplicate.
func TestInventoryOllamaLatestFallback(t *testing.T) {
	srv := ollamaServer(t, []string{"llama3:latest"}, []string{"llama3:latest"})
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/llama3", ProviderID: "ollama", ModelName: "llama3"}},
	}
	snap := inventory(cfg, testClient)
	e, ok := byModelID(snap, "ollama/llama3")
	if !ok || e.Artifact != "llama3:latest" || !e.Running || len(snap.Entries) != 1 {
		t.Errorf("entry = %+v ok=%v entries=%d", e, ok, len(snap.Entries))
	}
}

// TestInventoryOllamaDownWhenDaemonDiesBetweenProbes covers the daemon dying
// between the two ollama probes: /api/tags answers (so status is Partial, not
// Unreachable) but /api/ps is refused. That refusal still proves nothing is
// listening, so the family must be Down — otherwise `wt litellm sync`, which
// removes routes only for Down families, would keep routes pointing at a dead
// daemon. Both probes hit the same origin, so a refused second probe can only
// mean the server went away.
func TestInventoryOllamaDownWhenDaemonDiesBetweenProbes(t *testing.T) {
	srv := ollamaServer(t, []string{"x:1b"}, nil)
	// /api/tags reaches the live stub; /api/ps is answered with a real
	// connection-refused error, as if the daemon had just exited.
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/ps" {
				return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
			}
			return http.DefaultTransport.RoundTrip(r)
		}),
	}

	cfg := &config.Config{Providers: []config.Provider{localProvider("ollama", srv.URL, "")}}
	snap := inventory(cfg, client)
	if snap.Providers["ollama"] != StatusPartial {
		t.Fatalf("status = %q, want partial (tags answered, ps failed)", snap.Providers["ollama"])
	}
	if !snap.Down["ollama"] {
		t.Errorf("Down = %v, want ollama down (its /api/ps was refused)", snap.Down)
	}
}

// TestInventoryProviderDownDoesNotFailOthers verifies a dead ollama daemon is
// reported "unreachable" (its registered models still list, not running) while
// omlx discovery is unaffected — one provider must never blank the whole
// selector.
func TestInventoryProviderDownDoesNotFailOthers(t *testing.T) {
	dead := ollamaServer(t, nil, nil)
	deadURL := dead.URL
	dead.Close()
	root := t.TempDir()
	mkdirs(t, root, "Qwen3.8-27B-4bit")
	up := modelsServer(t)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", deadURL, ""), localProvider("omlx", up.URL, root)},
		Models:    []config.Model{{ID: "ollama/x:1b", ProviderID: "ollama", ModelName: "x:1b"}},
	}
	snap := inventory(cfg, testClient)
	if snap.Providers["ollama"] != StatusUnreachable || snap.Providers["omlx"] != StatusOK {
		t.Errorf("statuses = %v", snap.Providers)
	}
	if e, ok := byModelID(snap, "ollama/x:1b"); !ok || e.Running {
		t.Errorf("registered ollama entry = %+v ok=%v", e, ok)
	}
	if _, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Qwen3.8-27B-4bit")); !ok {
		t.Error("omlx discovery missing")
	}
}

// TestInventoryOmlx6bitRowSharesServerAndDirs verifies a registered omlx-6bit
// model matches a dir in the shared oMLX model directory and reads running from
// the shared server, with no duplicate discovered entry (omlx and omlx-6bit are
// one physical server).
func TestInventoryOmlx6bitRowSharesServerAndDirs(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Ornith-1.5-35B-A3B-MLX-6bit")
	srv := modelsServer(t, "Ornith-1.5-35B-A3B-MLX-6bit")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", srv.URL, root), localProvider("omlx-6bit", srv.URL, "")},
		Models:    []config.Model{{ID: "omlx-6bit/Ornith-1.5-35B-A3B-MLX-6bit", ProviderID: "omlx-6bit", ModelName: "Ornith-1.5-35B-A3B-MLX-6bit"}},
	}
	snap := inventory(cfg, testClient)
	if len(snap.Entries) != 1 || !snap.Entries[0].Registered || !snap.Entries[0].Running || snap.Entries[0].ProviderID != "omlx-6bit" {
		t.Errorf("entries = %+v", snap.Entries)
	}
}

// TestInventoryMlxLMServerRunningNoDiscovery verifies mlx_lm_server is
// "unsupported" for discovery (a target+draft pairing is not discoverable) yet a
// registered row reads running when the server serves anything.
func TestInventoryMlxLMServerRunningNoDiscovery(t *testing.T) {
	srv := modelsServer(t, "/some/target/path")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("mlx_lm_server", srv.URL, "")},
		Models:    []config.Model{{ID: "mlx_lm_server/x", ProviderID: "mlx_lm_server", ModelName: "x"}},
	}
	snap := inventory(cfg, testClient)
	if snap.Providers["mlx_lm_server"] != StatusUnsupported {
		t.Errorf("status = %q", snap.Providers["mlx_lm_server"])
	}
	if len(snap.Entries) != 1 || !snap.Entries[0].Running {
		t.Errorf("entries = %+v", snap.Entries)
	}
}

// TestInventoryIgnoresCloudModelsAndProviders verifies cloud-located providers
// and models are never probed or listed — the inventory is local-only.
func TestInventoryIgnoresCloudModelsAndProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}, {ID: "ollama", Location: config.LocationCloud}},
		Models:    []config.Model{{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x"}, {ID: "ollama/k:cloud", ProviderID: "ollama", ModelName: "k:cloud", Location: config.LocationCloud}},
	}
	snap := inventory(cfg, testClient)
	if len(snap.Entries) != 0 || len(snap.Providers) != 0 {
		t.Errorf("snapshot = %+v, want empty", snap)
	}
}

// TestInventoryProbesProvidersConcurrently verifies the probe round runs the
// providers in parallel: each server holds its response until BOTH have been
// hit, so a sequential implementation would take ~1.5s per provider while a
// concurrent one finishes well under a second. Concurrency is across provider
// families only (the ollama family does /api/tags then /api/ps sequentially,
// so its worst case is two probe timeouts); this keeps the selector's open
// latency near the slowest family, not the sum of all of them.
func TestInventoryProbesProvidersConcurrently(t *testing.T) {
	var hits int32
	barrier := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			deadline := time.Now().Add(1500 * time.Millisecond)
			for atomic.LoadInt32(&hits) < 2 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			next.ServeHTTP(w, r)
		})
	}
	ollama := httptest.NewServer(barrier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	})))
	defer ollama.Close()
	omlx := httptest.NewServer(barrier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})))
	defer omlx.Close()
	cfg := &config.Config{Providers: []config.Provider{
		localProvider("ollama", ollama.URL, ""), localProvider("omlx", omlx.URL, t.TempDir()),
	}}
	start := time.Now()
	inventory(cfg, testClient)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("inventory took %v, want < 1s (providers must probe concurrently)", elapsed)
	}
}

// TestInventoryMlxLMServerAmbiguousWithMultipleModels verifies that when
// several mlx_lm_server pairings are registered and the served name matches
// none of them, no entry is reported Running — a bare non-empty /v1/models
// cannot say which pairing is serving, and a wrong Running=true would make a
// caller target the wrong model. A served name that does match one still wins.
func TestInventoryMlxLMServerAmbiguousWithMultipleModels(t *testing.T) {
	models := []config.Model{
		{ID: "mlx_lm_server/a", ProviderID: "mlx_lm_server", ModelName: "model-a"},
		{ID: "mlx_lm_server/b", ProviderID: "mlx_lm_server", ModelName: "model-b"},
	}
	run := func(served string) Snapshot {
		srv := modelsServer(t, served)
		return inventory(&config.Config{
			Providers: []config.Provider{localProvider("mlx_lm_server", srv.URL, "")},
			Models:    models,
		}, testClient)
	}
	for _, e := range run("/opaque/path").Entries {
		if e.Running {
			t.Errorf("%s Running with ambiguous served name", e.ModelID)
		}
	}
	for _, e := range run("model-b").Entries {
		if want := e.ModelID == "mlx_lm_server/b"; e.Running != want {
			t.Errorf("%s Running=%v, want %v", e.ModelID, e.Running, want)
		}
	}
}

// TestInventoryModelLevelLocalOverrideIsProbed verifies a model that is local
// only via its own location override (its provider row is cloud) still gets its
// family probed, so it can read Running and its family appears in Providers.
func TestInventoryModelLevelLocalOverrideIsProbed(t *testing.T) {
	srv := modelsServer(t, "m1")
	prov := localProvider("omlx", srv.URL, t.TempDir())
	prov.Location = config.LocationCloud
	cfg := &config.Config{
		Providers: []config.Provider{prov},
		Models:    []config.Model{{ID: "omlx/m1", ProviderID: "omlx", ModelName: "m1", Location: config.LocationLocal}},
	}
	snap := inventory(cfg, testClient)
	if _, ok := snap.Providers["omlx"]; !ok {
		t.Fatalf("omlx family not probed: %+v", snap.Providers)
	}
	if len(snap.Entries) != 1 || !snap.Entries[0].Running {
		t.Errorf("entries = %+v, want one running", snap.Entries)
	}
}

// TestInventoryOllamaPsFailureIsPartial verifies an /api/ps failure (tags ok)
// reports StatusPartial rather than StatusOK, so consumers can tell "nothing
// loaded" from "running state unknown".
func TestInventoryOllamaPsFailureIsPartial(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"}]}`))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{Providers: []config.Provider{localProvider("ollama", srv.URL, "")}}
	snap := inventory(cfg, testClient)
	if snap.Providers["ollama"] != StatusPartial {
		t.Errorf("status = %q, want partial", snap.Providers["ollama"])
	}
	if len(snap.Entries) != 1 {
		t.Errorf("entries = %+v, want discovery to still work", snap.Entries)
	}
}

// TestInventoryOmlx6bitOnlyStillScansDir verifies a registry with only
// an omlx-6bit row still scans the omlx model dir (same physical server): the
// row's own model_dir is honoured and discovered dirs appear.
func TestInventoryOmlx6bitOnlyStillScansDir(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Some-Model-6bit")
	srv := modelsServer(t)
	cfg := &config.Config{Providers: []config.Provider{localProvider("omlx-6bit", srv.URL, root)}}
	snap := inventory(cfg, testClient)
	if _, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Some-Model-6bit")); !ok {
		t.Errorf("entries = %+v, want discovered Some-Model-6bit", snap.Entries)
	}
}

// TestInventoryArtifactKnownReflectsProbeOutcome verifies ArtifactKnown
// distinguishes "the provider answered and does not have this model" from "the
// probe could not tell". An unreachable family and mlx_lm_server (which cannot
// enumerate artifacts at all) must both report false, while a probe that
// answered reports true even when it found nothing. A consumer that reads an
// empty Artifact as "missing" without this flag hides every configured ollama
// model behind a transient daemon hiccup.
func TestInventoryArtifactKnownReflectsProbeOutcome(t *testing.T) {
	// The ollama daemon is down: nothing was discovered, and that is NOT the
	// same as "the model is not pulled".
	dead := ollamaServer(t, nil, nil)
	deadURL := dead.URL
	dead.Close()
	down := inventory(&config.Config{
		Providers: []config.Provider{localProvider("ollama", deadURL, "")},
		Models:    []config.Model{{ID: "ollama/x:1b", ProviderID: "ollama", ModelName: "x:1b"}},
	}, testClient)
	if st := down.Providers["ollama"]; st != StatusUnreachable {
		t.Fatalf("probe status = %q, want unreachable", st)
	}
	if e, ok := byModelID(down, "ollama/x:1b"); !ok || e.ArtifactKnown {
		t.Errorf("unreachable ollama entry = %+v ok=%v, want ArtifactKnown=false", e, ok)
	}

	// The probe DID answer and found nothing: the model is genuinely absent.
	up := inventory(&config.Config{
		Providers: []config.Provider{localProvider("ollama", ollamaServer(t, []string{"other:1b"}, nil).URL, "")},
		Models:    []config.Model{{ID: "ollama/gone:7b", ProviderID: "ollama", ModelName: "gone:7b"}},
	}, testClient)
	if e, ok := byModelID(up, "ollama/gone:7b"); !ok || !e.ArtifactKnown || e.Artifact != "" {
		t.Errorf("answered entry = %+v ok=%v, want ArtifactKnown=true with empty Artifact", e, ok)
	}

	// mlx_lm_server serves one target+draft pairing per process and wt cannot
	// reconstruct the served name, so its registered rows are always unknown.
	mlx := inventory(&config.Config{
		Providers: []config.Provider{localProvider("mlx_lm_server", modelsServer(t, "/some/target/path").URL, "")},
		Models:    []config.Model{{ID: "mlx_lm_server/x", ProviderID: "mlx_lm_server", ModelName: "x"}},
	}, testClient)
	if e, ok := byModelID(mlx, "mlx_lm_server/x"); !ok || e.ArtifactKnown {
		t.Errorf("mlx_lm_server entry = %+v ok=%v, want ArtifactKnown=false", e, ok)
	}
}

// TestInventoryEntriesCarryProviderSideModelName verifies every entry exposes
// the provider-side name (registered: the registry model_name; discovered: the
// artifact name). The lifecycle engine matches a start target against running
// entries by this name, and an entry with only a registry id cannot be compared.
func TestInventoryEntriesCarryProviderSideModelName(t *testing.T) {
	srv := ollamaServer(t, []string{"gemma4:9b", "other:1b"}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/gemma", ProviderID: "ollama", ModelName: "gemma4:9b"}},
	}
	snap := inventory(cfg, testClient)
	reg, _ := byModelID(snap, "ollama/gemma")
	if reg.ModelName != "gemma4:9b" {
		t.Errorf("registered ModelName = %q, want gemma4:9b", reg.ModelName)
	}
	disc, _ := byModelID(snap, config.DiscoveredModelID("ollama", "other:1b"))
	if disc.ModelName != "other:1b" {
		t.Errorf("discovered ModelName = %q, want other:1b", disc.ModelName)
	}
}

// TestFamilyExported verifies the exported Family maps provider ids to probe
// families, with omlx-6bit sharing omlx's family and unknown ids returning "" —
// the lifecycle engine uses it to decide which backend and occupancy domain a
// target belongs to.
func TestFamilyExported(t *testing.T) {
	cases := map[string]string{"ollama": "ollama", "omlx": "omlx", "omlx-6bit": "omlx", "mtplx": "mtplx", "mlx_lm_server": "mlx_lm_server", "llamacpp": "", "": ""}
	for id, want := range cases {
		if got := Family(id); got != want {
			t.Errorf("Family(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestInventoryOmlxProbeFailureIsPartial verifies an unanswerable /v1/models
// leaves the omlx family StatusPartial rather than StatusOK-with-no-models.
// The lifecycle engine reads that status to decide whether Running can be
// trusted; reporting OK would let it treat a stalled daemon as empty and
// replace the model that daemon is serving.
func TestInventoryOmlxProbeFailureIsPartial(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "some-model")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer down.Close()
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", down.URL, dir)},
		Models:    []config.Model{{ID: "omlx/some-model", ProviderID: "omlx", ModelName: "some-model"}},
	}
	snap := inventory(cfg, testClient)
	if got := snap.Providers["omlx"]; got != StatusPartial {
		t.Errorf("omlx status with an unusable /v1/models = %q, want %q", got, StatusPartial)
	}
	// The model-dir scan still succeeded, so artifacts remain trustworthy.
	if en, ok := byModelID(snap, "omlx/some-model"); !ok || !en.ArtifactKnown {
		t.Errorf("probe failure must not make artifact discovery untrustworthy: %+v ok=%v", en, ok)
	}
}

// TestInventoryDownOnlyForRefusedConnections pins the Down signal: a closed
// port (connection refused) marks its family Down for both omlx (Partial) and
// ollama (Unreachable), while a live server that answers 500 is Partial but
// NOT Down. `wt litellm sync` removes routes only for Down families, so
// conflating "erroring" with "not running" would delete a live route.
func TestInventoryDownOnlyForRefusedConnections(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "some-model")
	gone := modelsServer(t)
	goneURL := gone.URL
	gone.Close()
	erroring := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer erroring.Close()
	deadOllama := ollamaServer(t, nil, nil)
	deadOllamaURL := deadOllama.URL
	deadOllama.Close()

	cfg := &config.Config{Providers: []config.Provider{localProvider("omlx", goneURL, dir), localProvider("ollama", deadOllamaURL, "")}}
	snap := inventory(cfg, testClient)
	if !snap.Down["omlx"] || !snap.Down["ollama"] {
		t.Errorf("Down = %v, want omlx and ollama down (connections refused)", snap.Down)
	}

	cfg = &config.Config{Providers: []config.Provider{localProvider("omlx", erroring.URL, dir)}}
	snap = inventory(cfg, testClient)
	if snap.Providers["omlx"] != StatusPartial || snap.Down["omlx"] {
		t.Errorf("erroring server: status=%q down=%v, want partial and not down", snap.Providers["omlx"], snap.Down["omlx"])
	}
}

// TestInventoryOmlxEmptyAnswerIsOK verifies a server that answers with an
// empty model list is StatusOK, not StatusPartial. That case is "nothing is
// loaded", which must start normally without a confirmation prompt.
func TestInventoryOmlxEmptyAnswerIsOK(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "some-model")
	up := modelsServer(t) // answers {"data":[]}
	defer up.Close()
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", up.URL, dir)},
		Models:    []config.Model{{ID: "omlx/some-model", ProviderID: "omlx", ModelName: "some-model"}},
	}
	if got := inventory(cfg, testClient).Providers["omlx"]; got != StatusOK {
		t.Errorf("omlx status with an empty model list = %q, want %q", got, StatusOK)
	}
}

// TestFamilyOriginPortAgreesWithOrigin verifies the port returned alongside an
// origin is the port that origin actually addresses, including when a registry
// base_url omits one. A caller passing the port to a command line while the
// origin says something else spawns a server on one port and polls another,
// which shows up as a full-length timeout instead of a failure.
func TestFamilyOriginPortAgreesWithOrigin(t *testing.T) {
	// No port in the registry value: the family default must be applied to the
	// origin too, not just to the returned port.
	bare := &config.Config{Providers: []config.Provider{localProvider("mtplx", "http://localhost", "")}}
	origin, port, err := FamilyOriginPort(bare, "mtplx")
	if err != nil {
		t.Fatalf("FamilyOriginPort: %v", err)
	}
	if origin != "http://localhost:8003" || port != 8003 {
		t.Errorf("bare origin: origin=%q port=%d, want http://localhost:8003 and 8003", origin, port)
	}
	if u, err := url.Parse(origin); err != nil || u.Port() != strconv.Itoa(port) {
		t.Errorf("origin %q does not address the returned port %d", origin, port)
	}

	// An explicit port wins, and is preserved in the origin.
	explicit := &config.Config{Providers: []config.Provider{localProvider("mtplx", "http://127.0.0.1:9123", "")}}
	origin, port, err = FamilyOriginPort(explicit, "mtplx")
	if err != nil {
		t.Fatalf("FamilyOriginPort: %v", err)
	}
	if origin != "http://127.0.0.1:9123" || port != 9123 {
		t.Errorf("explicit origin: origin=%q port=%d, want http://127.0.0.1:9123 and 9123", origin, port)
	}

	// The registry-free default still resolves to the family default.
	if origin, port, err = FamilyOriginPort(&config.Config{}, "omlx"); err != nil || port != 8000 {
		t.Errorf("default omlx: origin=%q port=%d err=%v, want 8000 and no error", origin, port, err)
	}
}
