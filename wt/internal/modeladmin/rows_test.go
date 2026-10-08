package modeladmin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func local(id string) config.Provider {
	return config.Provider{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}
}

// rowsConfig is a registry with one model of each kind model management
// lists: cloud, omlx on disk, omlx missing, ollama, a local_path model, a
// pairing, a model of a provider wt has no probe for, and one whose location
// does not resolve.
func rowsConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			local("ollama"), local("omlx"), local("mlx_lm_server"), local("llamacpp"),
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "nowhere", Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "openrouter/qwen--qwen3.8", Family: "qwen", ProviderID: "openrouter", ModelName: "qwen/qwen3.8", Tags: []string{"code"}},
			{ID: "omlx/Qwen-4bit", Family: "qwen", ProviderID: "omlx", ModelName: "Qwen-4bit"},
			{ID: "omlx/Gone-4bit", Family: "qwen", ProviderID: "omlx", ModelName: "Gone-4bit"},
			{ID: "ollama/gemma4:9b", Family: "gemma", ProviderID: "ollama", ModelName: "gemma4:9b"},
			{ID: "omlx/mine", Family: "qwen", ProviderID: "omlx", ModelName: "mine-4bit", Fetch: config.ModelArtifact{LocalPath: "/models/mine-4bit"}},
			{ID: "mlx_lm_server/T+draft-D", Family: "qwen", ProviderID: "mlx_lm_server", ModelName: "T+draft-D",
				Fetch: config.ModelArtifact{Repo: "org/T"}, Draft: config.ModelArtifact{LocalPath: "/models/D"}},
			{ID: "llamacpp/old", Family: "old", ProviderID: "llamacpp", ModelName: "old.gguf"},
			{ID: "nowhere/x", Family: "zz", ProviderID: "nowhere", ModelName: "x"},
		},
	}
}

func rowsSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK, "mlx_lm_server": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Qwen-4bit", ModelName: "Qwen-4bit", Artifact: "Qwen-4bit", Registered: true, ArtifactKnown: true, Running: true, Path: "/omlx/Qwen-4bit"},
			{ProviderID: "omlx", ModelID: "omlx/Gone-4bit", ModelName: "Gone-4bit", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/mine", ModelName: "mine-4bit", Registered: true, ArtifactKnown: true, Running: true, Loading: true},
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true, Size: 5_000_000_000},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/T+draft-D", ModelName: "T+draft-D", Registered: true},
			// The inventory lists every local registry model, a provider it
			// has no probe for included: an entry with nothing known.
			{ProviderID: "llamacpp", ModelID: "llamacpp/old", ModelName: "old.gguf", Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/found:1b", ModelName: "found:1b", Artifact: "found:1b", ArtifactKnown: true, Size: 7},
			{ProviderID: "omlx", ModelID: "omlx/Found-8bit", ModelName: "Found-8bit", Artifact: "Found-8bit", ArtifactKnown: true, Path: "/omlx/Found-8bit"},
		},
	}
}

func rowByID(t *testing.T, rows []Row, id string) Row {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q in %d rows", id, len(rows))
	return Row{}
}

// TestRowsListEveryModelWithItsStatus verifies one row per registry model
// and per discovered model, each with the status, running state, path and
// size the spec gives it. This table is all `wt model list` and the Models
// tab show; a row left out here is a model the user cannot see, edit or
// remove.
func TestRowsListEveryModelWithItsStatus(t *testing.T) {
	old := statLocalPath
	statLocalPath = func(p string) bool { return p == "/models/mine-4bit" }
	t.Cleanup(func() { statLocalPath = old })

	rows := Rows(rowsConfig(), rowsSnapshot())
	if len(rows) != 10 {
		t.Fatalf("%d rows, want the 8 registry models and the 2 discovered ones", len(rows))
	}
	cases := []struct {
		id      string
		loc     string
		status  Status
		running string
		path    string
		size    int64
	}{
		{"openrouter/qwen--qwen3.8", "cloud", StatusOK, RunningNo, "", 0},
		{"omlx/Qwen-4bit", "local", StatusOK, RunningRun, "/omlx/Qwen-4bit", 0},
		{"omlx/Gone-4bit", "local", StatusMissing, RunningNo, "", 0},
		{"ollama/gemma4:9b", "local", StatusOK, RunningNo, "", 5_000_000_000},
		// A local_path row: the scan cannot find it, so presence is a stat
		// and the path is the registry's own.
		{"omlx/mine", "local", StatusOK, RunningLoad, "/models/mine-4bit", 0},
		{"mlx_lm_server/T+draft-D", "local", StatusNone, RunningNo, "", 0},
		// No probe exists for llamacpp: nothing can vouch for it.
		{"llamacpp/old", "local", StatusUnknown, RunningUnknown, "", 0},
		// The location does not resolve: listed so it can be repaired.
		{"nowhere/x", "", StatusUnknown, RunningNo, "", 0},
		{"ollama/found:1b", "local", StatusNew, RunningNo, "", 7},
		{"omlx/Found-8bit", "local", StatusNew, RunningNo, "/omlx/Found-8bit", 0},
	}
	for _, c := range cases {
		r := rowByID(t, rows, c.id)
		if r.Location != c.loc || r.Status != c.status || r.Running != c.running || r.Path != c.path || r.Size != c.size {
			t.Errorf("%s = loc %q status %q running %q path %q size %d; want %q %q %q %q %d",
				c.id, r.Location, r.Status, r.Running, r.Path, r.Size, c.loc, c.status, c.running, c.path, c.size)
		}
		if want := c.status != StatusNew; r.Registered != want {
			t.Errorf("%s: Registered = %v, want %v", c.id, r.Registered, want)
		}
	}
	pair := rowByID(t, rows, "mlx_lm_server/T+draft-D")
	if !pair.Pairing() || pair.Target != "org/T" || pair.Draft != "/models/D" {
		t.Errorf("pairing = %+v, want target org/T and draft /models/D", pair)
	}
	if rows[0].Family != "gemma" || rows[len(rows)-2].ID != "ollama/found:1b" {
		t.Errorf("order: first family %q, second-last id %q; want registry rows by family, discovered rows last", rows[0].Family, rows[len(rows)-2].ID)
	}
}

// TestRowsMarkAnUntrustedProbe verifies the rows of a family whose probe
// failed: status unknown when discovery failed, and running "?" unless the
// server refused the connection. A blank RUNNING cell there would say
// "stopped" about a model that may be serving.
func TestRowsMarkAnUntrustedProbe(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{local("ollama"), local("omlx"), local("mlx_lm_server")},
		Models: []config.Model{
			{ID: "ollama/a:1", Family: "a", ProviderID: "ollama", ModelName: "a:1"},
			{ID: "omlx/b", Family: "b", ProviderID: "omlx", ModelName: "b"},
			{ID: "mlx_lm_server/p", Family: "p", ProviderID: "mlx_lm_server", ModelName: "p"},
		},
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "omlx": localmodels.StatusPartial, "mlx_lm_server": localmodels.StatusPartial,
		},
		Down:      map[string]bool{"omlx": true},
		Ambiguous: map[string]bool{"mlx_lm_server": true},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/a:1", ModelName: "a:1", Registered: true},
			{ProviderID: "omlx", ModelID: "omlx/b", ModelName: "b", Artifact: "b", Registered: true, ArtifactKnown: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p", Registered: true},
		},
	}
	rows := Rows(cfg, snap)
	if r := rowByID(t, rows, "ollama/a:1"); r.Status != StatusUnknown || r.Running != RunningUnknown {
		t.Errorf("unreachable ollama = %q / %q, want unknown / ?", r.Status, r.Running)
	}
	if r := rowByID(t, rows, "omlx/b"); r.Status != StatusOK || r.Running != RunningNo {
		t.Errorf("omlx that refused the connection = %q / %q, want ok and not running", r.Status, r.Running)
	}
	if r := rowByID(t, rows, "mlx_lm_server/p"); r.Status != StatusNone || r.Running != RunningUnknown {
		t.Errorf("ambiguous pairing = %q / %q, want - / ?", r.Status, r.Running)
	}
}

// TestWeightsNote verifies what `wt model rm` tells the user is left behind
// for each kind of row. wt never deletes weights, so this line is the only
// thing between a removed row and tens of gigabytes nobody remembers.
func TestWeightsNote(t *testing.T) {
	old := statLocalPath
	statLocalPath = func(string) bool { return false }
	t.Cleanup(func() { statLocalPath = old })
	rows := Rows(rowsConfig(), rowsSnapshot())
	want := map[string]string{
		"openrouter/qwen--qwen3.8": "",
		"omlx/Qwen-4bit":           "weights are still at /omlx/Qwen-4bit",
		"omlx/Gone-4bit":           "no weights were found on disk",
		"ollama/gemma4:9b":         "still pulled in ollama (`ollama rm gemma4:9b` deletes it)",
		"omlx/mine":                "nothing was found at /models/mine-4bit",
		"mlx_lm_server/T+draft-D":  "target org/T and draft /models/D are untouched",
		"llamacpp/old":             "wt could not tell where its weights are",
		"nowhere/x":                "",
	}
	for id, note := range want {
		if got := WeightsNote(rowByID(t, rows, id)); got != note {
			t.Errorf("WeightsNote(%s) = %q, want %q", id, got, note)
		}
	}
	// An ollama that could not be asked: nothing saw the model pulled, so
	// the note must not say it is.
	unasked := Row{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: "local", Registered: true, Status: StatusUnknown}
	if got := WeightsNote(unasked); got != "wt could not tell where its weights are" {
		t.Errorf("WeightsNote for an unreachable ollama = %q, want the could-not-tell line", got)
	}
}

// TestFormatSize pins the sizes the listing and the Models tab print:
// decimal units as `ollama list` shows them, and "-" for a size wt does not
// know, never "0 B".
func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "-", -1: "-", 7: "7 B", 4_200: "4 kB", 734_000_000: "734 MB", 5_225_388_164: "5.2 GB", 27_000_000_000: "27.0 GB",
		// Just under a unit: the next unit, never "1000" of this one.
		999_700: "1 MB", 999_700_000: "1.0 GB",
	} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRowsShowThePathTheInventoryGives verifies Rows copies a registered
// entry's Path as it is, and adds no rule of its own: whether a scanned
// directory is the model's own is decided where the match is made
// (localmodels.Entry.Path, pinned by
// TestInventoryPathIsTheModelsOwnDirectoryOrEmpty). An entry the inventory
// left without a path is a row with none, whose note says wt could not tell
// where the weights are; a second copy of the rule here could only drift from
// the first. The second half runs the real inventory over a directory where
// the row's name exists only under another organization, so the #266 case is
// seen end to end: the model is there, and no path is printed for it.
func TestRowsShowThePathTheInventoryGives(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{local("omlx")},
		Models: []config.Model{
			{ID: "omlx/given", Family: "q", ProviderID: "omlx", ModelName: "A", Fetch: config.ModelArtifact{Repo: "org-a/A"}},
			{ID: "omlx/withheld", Family: "q", ProviderID: "omlx", ModelName: "B", Fetch: config.ModelArtifact{Repo: "org-a/B"}},
		},
	}
	entry := func(id, name, path string) localmodels.Entry {
		return localmodels.Entry{ProviderID: "omlx", ModelID: id, ModelName: name, Artifact: name, Registered: true, ArtifactKnown: true, Path: path}
	}
	rows := Rows(cfg, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			// Not a directory this package would have chosen: Rows does not judge it.
			entry("omlx/given", "A", "/anywhere/the-inventory/says"),
			entry("omlx/withheld", "B", ""),
		},
	})
	if got := rowByID(t, rows, "omlx/given"); got.Path != "/anywhere/the-inventory/says" || got.Status != StatusOK {
		t.Errorf("given = %+v, want the inventory's path unchanged", got)
	}
	withheld := rowByID(t, rows, "omlx/withheld")
	if withheld.Path != "" || withheld.Status != StatusOK {
		t.Errorf("withheld = %+v, want an ok row with no path", withheld)
	}
	if got := WeightsNote(withheld); got != "wt could not tell where its weights are" {
		t.Errorf("WeightsNote for a withheld path = %q, want the could-not-tell line", got)
	}

	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := localmodels.MakeOmlxModelsForTest(dir, "org-a/A", "org-b/B"); err != nil {
		t.Fatal(err)
	}
	// A server that is not there: the directory scan is what fills Path.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	cfg.Providers[0].ModelDir = dir
	cfg.Providers[0].Auth.BaseURL = srv.URL + "/v1"
	rows = Rows(cfg, localmodels.Inventory(cfg))
	if got := rowByID(t, rows, "omlx/given"); got.Path != filepath.Join(dir, "org-a", "A") || got.Status != StatusOK {
		t.Errorf("a model in its own organization's folder = %+v, want its directory", got)
	}
	if got := rowByID(t, rows, "omlx/withheld"); got.Path != "" || got.Status != StatusOK {
		t.Errorf("a model found only under another organization = %+v, want ok with no path", got)
	}
}

// TestRowsGiveADuplicatedIDItsOwnEntries verifies two registry rows with one
// id each take their own inventory entry, in the registry's order. The list
// runs on a registry that does not validate, and a copied row is one of the
// gaps it is there to show; matched by id alone, both rows took the second
// entry, the one the inventory left without the artifact, and weights that
// are on disk read "missing" on both lines.
func TestRowsGiveADuplicatedIDItsOwnEntries(t *testing.T) {
	dup := config.Model{ID: "omlx/Flat", Family: "q", ProviderID: "omlx", ModelName: "Flat"}
	cfg := &config.Config{Providers: []config.Provider{local("omlx")}, Models: []config.Model{dup, dup}}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Flat", ModelName: "Flat", Artifact: "Flat", Registered: true, ArtifactKnown: true, Path: "/omlx/Flat"},
			{ProviderID: "omlx", ModelID: "omlx/Flat", ModelName: "Flat", Registered: true, ArtifactKnown: true},
		},
	}
	rows := Rows(cfg, snap)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want both registry rows: %+v", len(rows), rows)
	}
	if rows[0].Status != StatusOK || rows[0].Path != "/omlx/Flat" {
		t.Errorf("first row = %q at %q, want ok with the directory the scan matched to it", rows[0].Status, rows[0].Path)
	}
	if rows[1].Status != StatusMissing || rows[1].Path != "" {
		t.Errorf("second row = %q at %q, want missing: the first row holds the artifact", rows[1].Status, rows[1].Path)
	}
}

// TestRowsPairADuplicatedIDByProvider verifies two registry rows that share
// an id under different providers each take the entry of their own provider,
// whatever order the inventory lists them in. The inventory sorts its entries
// by provider id, so omlx comes before omlx-6bit there even when the registry
// has them the other way round; queued by id alone, each row took the other's
// entry, and a row for org-a's model printed the directory of a different
// model in org-b as its own weights (the #266 symptom).
func TestRowsPairADuplicatedIDByProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{local("omlx"), local("omlx-6bit")},
		Models: []config.Model{
			{ID: "dup/x", Family: "q", ProviderID: "omlx-6bit", ModelName: "DupA", Fetch: config.ModelArtifact{Repo: "org-a/DupA"}},
			{ID: "dup/x", Family: "q", ProviderID: "omlx", ModelName: "DupB", Fetch: config.ModelArtifact{Repo: "org-b/DupB"}},
		},
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		// As localmodels.Inventory returns them: sorted by provider id, and
		// DupA's path withheld (its directory is org-b's copy of the name).
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "dup/x", ModelName: "DupB", Artifact: "DupB", Registered: true, ArtifactKnown: true, Running: true, Path: "/omlx/org-b/DupB"},
			{ProviderID: "omlx-6bit", ModelID: "dup/x", ModelName: "DupA", Artifact: "DupA", Registered: true, ArtifactKnown: true},
		},
	}
	rows := Rows(cfg, snap)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want both registry rows: %+v", len(rows), rows)
	}
	a, b := rows[0], rows[1]
	if a.ModelName != "DupA" || b.ModelName != "DupB" {
		t.Fatalf("rows = %q, %q, want DupA then DupB (the registry's order)", a.ModelName, b.ModelName)
	}
	if a.Path != "" || a.Running != RunningNo {
		t.Errorf("DupA row: path %q running %q, want neither: those are DupB's", a.Path, a.Running)
	}
	if b.Path != "/omlx/org-b/DupB" || b.Running != RunningRun {
		t.Errorf("DupB row: path %q running %q, want its own directory and run", b.Path, b.Running)
	}
}

// TestRowsNeverListTwoRowsWithOneID verifies a discovered artifact whose id
// a registry row already holds is left out: a row omlx/Foo whose model_name
// matches nothing on disk, beside an on-disk Foo, would otherwise be listed
// twice under omlx/Foo, once missing and once new. `wt model rm omlx/Foo`
// and the Models tab both act on a row by its id.
func TestRowsNeverListTwoRowsWithOneID(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{local("omlx")},
		Models:    []config.Model{{ID: "omlx/Foo", Family: "q", ProviderID: "omlx", ModelName: "Renamed-4bit"}},
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Foo", ModelName: "Renamed-4bit", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/Foo", ModelName: "Foo", Artifact: "Foo", ArtifactKnown: true, Path: "/omlx/Foo"},
			{ProviderID: "omlx", ModelID: "omlx/Bar", ModelName: "Bar", Artifact: "Bar", ArtifactKnown: true, Path: "/omlx/Bar"},
		},
	}
	rows := Rows(cfg, snap)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want the registry row and the one discovered model with a free id: %+v", len(rows), rows)
	}
	if r := rowByID(t, rows, "omlx/Foo"); !r.Registered || r.Status != StatusMissing {
		t.Errorf("omlx/Foo = %+v, want the registered, missing row", r)
	}
	if r := rowByID(t, rows, "omlx/Bar"); r.Status != StatusNew {
		t.Errorf("omlx/Bar status = %q, want new", r.Status)
	}
}
