package localmodels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestInventoryEntriesCarryTheModelDirectory verifies an omlx model (flat, and
// inside an organization folder) and an mtplx model each report the directory
// the scan found them in, for a registered row and a discovered one alike,
// and that an omlx name found under two organizations is one entry with no
// directory at all. `wt model rm` prints that path as "the weights are still
// here"; a wrong or empty one sends the user to delete the wrong directory,
// or none, and a guess between two organizations is the #266 case.
func TestInventoryEntriesCarryTheModelDirectory(t *testing.T) {
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	mkOmlxModels(t, omlxDir, "Flat-4bit", "mlx-community/Nested-6bit", "orgA/Same-4bit", "orgB/Same-4bit")
	mkdirs(t, mtplxDir, "Org--Model")
	omlx := &fakeOmlx{listed: []string{"Flat-4bit", "Nested-6bit", "Same-4bit"}, pool: map[string]bool{"Flat-4bit": false, "Nested-6bit": false, "Same-4bit": false}}
	mtplx := modelsServer(t)
	cfg := &config.Config{
		Providers: []config.Provider{
			localProvider("omlx", omlx.serve(t), omlxDir),
			localProvider("mtplx", mtplx.URL+"/v1", mtplxDir),
		},
		Models: []config.Model{{ID: "omlx/flat", ProviderID: "omlx", ModelName: "Flat-4bit"}},
	}
	snap := inventory(cfg, testClient)
	want := map[string]string{
		"omlx/flat":        filepath.Join(omlxDir, "Flat-4bit"),
		"omlx/Nested-6bit": filepath.Join(omlxDir, "mlx-community", "Nested-6bit"),
		"mtplx/Org/Model":  filepath.Join(mtplxDir, "Org--Model"),
		"omlx/Same-4bit":   "",
	}
	for id, path := range want {
		e, ok := byModelID(snap, id)
		if !ok || e.Path != path {
			t.Errorf("%s: Path = %q ok=%v, want %q", id, e.Path, ok, path)
		}
		if e.Size != 0 {
			t.Errorf("%s: Size = %d, want 0 (no directory is walked)", id, e.Size)
		}
	}
}

// TestScanOmlxModelPathsDropsAmbiguousPath verifies a name found in two
// organization folders is listed once with no path. A leaf-name match across
// organizations must never put another organization's directory on a row
// that `wt model rm` then names as "the weights are still here" (#266).
func TestScanOmlxModelPathsDropsAmbiguousPath(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "orgA/Same-4bit", "orgB/Same-4bit", "Solo-4bit")
	names, paths, err := scanOmlxModelPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "Same-4bit" || names[1] != "Solo-4bit" {
		t.Errorf("names = %v, want Same-4bit once and Solo-4bit", names)
	}
	if p, ok := paths["Same-4bit"]; ok {
		t.Errorf("ambiguous name kept path %q, want none", p)
	}
	if paths["Solo-4bit"] != filepath.Join(dir, "Solo-4bit") {
		t.Errorf("Solo path = %q", paths["Solo-4bit"])
	}
}

// TestScanOmlxModelPathsWhenTheDirectoryIsItselfAModel verifies omlx's
// fallback: a model directory that holds no model but is one is listed under
// its own name, with itself as the path. That path is what `wt model list`
// and `wt model rm` print for the one model of such a setup; without it the
// row would say wt cannot tell where the weights are while the scan knows.
func TestScanOmlxModelPathsWhenTheDirectoryIsItselfAModel(t *testing.T) {
	root := t.TempDir()
	mkOmlxModels(t, root, "Only-4bit")
	dir := filepath.Join(root, "Only-4bit")
	names, paths, err := scanOmlxModelPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "Only-4bit" || len(paths) != 1 || paths["Only-4bit"] != dir {
		t.Errorf("names = %v, paths = %v; want Only-4bit at %s", names, paths, dir)
	}
}

// TestInventoryOllamaEntriesCarryTheirSize verifies an ollama entry's Size is
// the `size` /api/tags reports and its Path is empty: ollama keeps blobs, not
// a directory per model. `wt model list --json` reports both, and a made-up
// path would name a directory that does not exist.
func TestInventoryOllamaEntriesCarryTheirSize(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{"name": "qwen3:8b", "size": 5225388164},
			{"name": "nosize:1b"},
		}})
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/qwen3:8b", ProviderID: "ollama", ModelName: "qwen3:8b"}},
	}
	snap := inventory(cfg, testClient)
	if e, ok := byModelID(snap, "ollama/qwen3:8b"); !ok || e.Size != 5225388164 || e.Path != "" {
		t.Errorf("registered = %+v ok=%v, want Size 5225388164 and no Path", e, ok)
	}
	if e, ok := byModelID(snap, "ollama/nosize:1b"); !ok || e.Size != 0 {
		t.Errorf("no size reported = %+v ok=%v, want Size 0", e, ok)
	}
}

// TestInventoryMissingModelHasNoPath verifies a registered model the scan did
// not find has no Path. A path guessed from the model directory and the name
// would tell the user weights exist where there are none.
func TestInventoryMissingModelHasNoPath(t *testing.T) {
	dir := t.TempDir()
	omlx := &fakeOmlx{}
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", omlx.serve(t), dir)},
		Models:    []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "Gone-4bit"}},
	}
	e, ok := byModelID(inventory(cfg, testClient), "omlx/gone")
	if !ok || e.Path != "" || e.Artifact != "" || !e.ArtifactKnown {
		t.Errorf("entry = %+v ok=%v, want a known-missing model with no Path", e, ok)
	}
}
