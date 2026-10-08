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

// TestInventoryPathIsTheModelsOwnDirectoryOrEmpty pins the #266 rule where
// the match is made: a registered entry's Path is the directory of the model
// the row names, or "". The probe matches by leaf name, so a row for
// org-a/Name is matched to org-b/Name when that is the only copy on disk; the
// entry still says the model is there (Artifact), and withholds the path. A
// path wt prints is a path someone may delete, and Entry.Path is exported:
// held one layer up, in modeladmin, the rule protected `wt model list` and
// left every other reader of a snapshot to print another organization's
// weights as this model's.
func TestInventoryPathIsTheModelsOwnDirectoryOrEmpty(t *testing.T) {
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	mkOmlxModels(t, omlxDir,
		"Direct-4bit", "org-a/Own-4bit", "org-b/Other-4bit",
		"org-a/NamedOwn-4bit", "org-b/Named-4bit", "org-b/Prec-4bit",
		"org-b/NoSlash-4bit", "org-b/NoOrg-4bit",
		"org-a/Twice-4bit", "org-b/Twice-4bit", "org-b/Found-4bit")
	mkdirs(t, mtplxDir, "OrgA--Own", "OrgB--Other", "OrgA--Named", "OrgB--Elsewhere", "OrgB--NoOrg", "Flat", "OrgB--Found")
	omlx, mtplx := &fakeOmlx{}, modelsServer(t)

	repo := func(r string) config.ModelArtifact { return config.ModelArtifact{Repo: r} }
	inOmlx := func(parts ...string) string { return filepath.Join(append([]string{omlxDir}, parts...)...) }
	cases := []struct {
		name      string
		model     config.Model
		wantFound bool // the entry names an artifact: the model is on disk
		wantPath  string
	}{
		// omlx: directly in the model directory, or in an organization folder.
		{"directly under the model dir", config.Model{ID: "omlx/direct", ProviderID: "omlx", ModelName: "Direct-4bit", Fetch: repo("org-a/Direct-4bit")}, true, inOmlx("Direct-4bit")},
		{"under the row's own organization", config.Model{ID: "omlx/own", ProviderID: "omlx", ModelName: "Own-4bit", Fetch: repo("org-a/Own-4bit")}, true, inOmlx("org-a", "Own-4bit")},
		{"under another organization", config.Model{ID: "omlx/other", ProviderID: "omlx", ModelName: "Other-4bit", Fetch: repo("org-a/Other-4bit")}, true, ""},
		{"model_name is org/name, its own folder", config.Model{ID: "omlx/named-own", ProviderID: "omlx", ModelName: "org-a/NamedOwn-4bit"}, true, inOmlx("org-a", "NamedOwn-4bit")},
		{"model_name is org/name, another folder", config.Model{ID: "omlx/named", ProviderID: "omlx", ModelName: "org-a/Named-4bit"}, true, ""},
		{"fetch.repo outranks model_name", config.Model{ID: "omlx/prec", ProviderID: "omlx", ModelName: "org-b/Prec-4bit", Fetch: repo("org-a/Prec-4bit")}, true, ""},
		// A repo with no "/" names no organization, so nothing contradicts
		// the match and the path is kept — as for a row with no fetch at all.
		{"fetch.repo without a slash", config.Model{ID: "omlx/noslash", ProviderID: "omlx", ModelName: "NoSlash-4bit", Fetch: repo("NoSlash-4bit")}, true, inOmlx("org-b", "NoSlash-4bit")},
		{"a row that names no organization", config.Model{ID: "omlx/noorg", ProviderID: "omlx", ModelName: "NoOrg-4bit"}, true, inOmlx("org-b", "NoOrg-4bit")},
		{"a name found in two directories", config.Model{ID: "omlx/twice", ProviderID: "omlx", ModelName: "Twice-4bit", Fetch: repo("org-a/Twice-4bit")}, true, ""},
		// mtplx: every model sits directly in the model directory as
		// <org>--<name>, so the directory's own name carries the organization.
		{"mtplx, its own organization", config.Model{ID: "mtplx/own", ProviderID: "mtplx", ModelName: "Own", Fetch: repo("OrgA/Own")}, true, filepath.Join(mtplxDir, "OrgA--Own")},
		{"mtplx, another organization", config.Model{ID: "mtplx/other", ProviderID: "mtplx", ModelName: "Other", Fetch: repo("OrgA/Other")}, true, ""},
		{"mtplx, model_name is org/name", config.Model{ID: "mtplx/named", ProviderID: "mtplx", ModelName: "OrgA/Named"}, true, filepath.Join(mtplxDir, "OrgA--Named")},
		// org/name against another organization's copy is not a match at all.
		{"mtplx, model_name of another organization", config.Model{ID: "mtplx/elsewhere", ProviderID: "mtplx", ModelName: "OrgA/Elsewhere"}, false, ""},
		{"mtplx, a row that names no organization", config.Model{ID: "mtplx/noorg", ProviderID: "mtplx", ModelName: "NoOrg"}, true, filepath.Join(mtplxDir, "OrgB--NoOrg")},
		{"mtplx, a directory that names no organization", config.Model{ID: "mtplx/flat", ProviderID: "mtplx", ModelName: "Flat", Fetch: repo("OrgA/Flat")}, true, filepath.Join(mtplxDir, "Flat")},
	}
	cfg := &config.Config{Providers: []config.Provider{
		localProvider("omlx", omlx.serve(t), omlxDir),
		localProvider("mtplx", mtplx.URL+"/v1", mtplxDir),
	}}
	for _, c := range cases {
		cfg.Models = append(cfg.Models, c.model)
	}
	snap := inventory(cfg, testClient)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := byModelID(snap, c.model.ID)
			if !ok || !e.Registered || !e.ArtifactKnown {
				t.Fatalf("entry = %+v ok=%v, want a registered, probed entry", e, ok)
			}
			if found := e.Artifact != ""; found != c.wantFound {
				t.Errorf("Artifact = %q, want found=%v: withholding a path must not change whether the model is on disk", e.Artifact, c.wantFound)
			}
			if e.Path != c.wantPath {
				t.Errorf("Path = %q, want %q", e.Path, c.wantPath)
			}
		})
	}
	// A discovered entry has no row to contradict the scan: it keeps the
	// directory it was found in, organization folder and all.
	for id, want := range map[string]string{
		"omlx/Found-4bit":  inOmlx("org-b", "Found-4bit"),
		"mtplx/OrgB/Found": filepath.Join(mtplxDir, "OrgB--Found"),
	} {
		if e, ok := byModelID(snap, id); !ok || e.Registered || e.Path != want {
			t.Errorf("discovered %s = %+v ok=%v, want Path %q", id, e, ok, want)
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
