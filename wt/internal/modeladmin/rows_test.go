package modeladmin

import (
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
}

// TestFormatSize pins the sizes the listing and the Models tab print:
// decimal units as `ollama list` shows them, and "-" for a size wt does not
// know, never "0 B".
func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "-", -1: "-", 7: "7 B", 4_200: "4 kB", 734_000_000: "734 MB", 5_225_388_164: "5.2 GB", 27_000_000_000: "27.0 GB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRowsKeepOnlyTheModelsOwnDirectory verifies a registered row shows the
// scanned directory only when it is plausibly its own: the probe matches by
// leaf name, so org-a/Name can be matched to org-b/Name, and `wt model rm`
// would then point at another organization's weights (closed issue #266). A
// discovered row has no repo to contradict and keeps the path.
func TestRowsKeepOnlyTheModelsOwnDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := home + "/.omlx/models"
	omlx := local("omlx")
	cfg := &config.Config{
		Providers: []config.Provider{omlx},
		Models: []config.Model{
			{ID: "omlx/own-org", Family: "q", ProviderID: "omlx", ModelName: "A", Fetch: config.ModelArtifact{Repo: "org-a/A"}},
			{ID: "omlx/other-org", Family: "q", ProviderID: "omlx", ModelName: "B", Fetch: config.ModelArtifact{Repo: "org-a/B"}},
			{ID: "omlx/flat", Family: "q", ProviderID: "omlx", ModelName: "C", Fetch: config.ModelArtifact{Repo: "org-a/C"}},
		},
	}
	entry := func(id, name, path string) localmodels.Entry {
		return localmodels.Entry{ProviderID: "omlx", ModelID: id, ModelName: name, Artifact: name, Registered: true, ArtifactKnown: true, Path: path}
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			entry("omlx/own-org", "A", dir+"/org-a/A"),
			entry("omlx/other-org", "B", dir+"/org-b/B"),
			entry("omlx/flat", "C", dir+"/C"),
			{ProviderID: "omlx", ModelID: "omlx/New", ModelName: "New", Artifact: "New", ArtifactKnown: true, Path: dir + "/org-b/New"},
		},
	}
	rows := Rows(cfg, snap)
	for id, want := range map[string]string{
		"omlx/own-org":   dir + "/org-a/A",
		"omlx/other-org": "",
		"omlx/flat":      dir + "/C",
		"omlx/New":       dir + "/org-b/New",
	} {
		if got := rowByID(t, rows, id).Path; got != want {
			t.Errorf("%s: Path = %q, want %q", id, got, want)
		}
	}
	// A provider row's own model_dir replaces the default one.
	cfg.Providers[0].ModelDir = "/custom/models"
	snap.Entries = []localmodels.Entry{entry("omlx/flat", "C", "/custom/models/C"), entry("omlx/other-org", "B", dir+"/B")}
	rows = Rows(cfg, snap)
	if got := rowByID(t, rows, "omlx/flat").Path; got != "/custom/models/C" {
		t.Errorf("flat model under a custom model_dir: Path = %q", got)
	}
	if got := rowByID(t, rows, "omlx/other-org").Path; got != "" {
		t.Errorf("a model directly under the default dir while model_dir is custom: Path = %q, want none", got)
	}
	if got := WeightsNote(rowByID(t, rows, "omlx/other-org")); got != "wt could not tell where its weights are" {
		t.Errorf("WeightsNote for a cross-organization match = %q, want the could-not-tell line", got)
	}
}
