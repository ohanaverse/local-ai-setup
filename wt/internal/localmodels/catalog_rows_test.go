// Tests for the host half of the model-selector row rules: which models become
// rows (CatalogRows), each row's live presence status, and the message for a
// pin with no row (MissingReason). They moved from internal/catalog with the
// code (Phase 3 WP2); what a row then does is still catalog's to test.
package localmodels

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func catalogTestCfg() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal},
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "mtplx", Location: config.LocationLocal},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx", "ollama"}}},
	}
	return cfg
}

func rowIDs(rows []catalog.Row) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Model.ID
	}
	return ids
}

// TestCatalogRowsIncludesConfiguredAndDiscoveredLocal verifies the three row
// sources: a configured cloud model, a configured local model (running or
// not), and a discovered on-disk model with no registry entry — the union
// the selector is meant to show.
func TestCatalogRowsIncludesConfiguredAndDiscoveredLocal(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "openrouter/cheap", ProviderID: "openrouter", ModelName: "cheap"},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"},
	}
	inv := &Snapshot{Entries: []Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc"},
	}}
	rows := CatalogRows(CatalogInput{Config: cfg, Agent: "claude", Models: models, Inventory: inv})

	if got := strings.Join(rowIDs(rows), ","); got != "openrouter/cheap,omlx/a,omlx/disc" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].Location != config.LocationCloud || rows[0].Status != catalog.StatusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].Running || rows[1].Status != catalog.StatusOK || rows[1].Discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.Discovered || d.Status != catalog.StatusNew || d.Running || d.Model.ModelName != "disc" || d.Location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestCatalogRowsHidesLocalModelsNotOnDisk verifies #179 Phase B's row rule: a
// registry local model gets a row only from its inventory entry. An overlay
// the probe confirmed is not on disk, and one with no inventory entry at all
// (e.g. its location could not be resolved), both get no row — the picker
// lists what exists, not what is configured. Agent supported_providers stays a hard constraint
// for discovered rows, HideDiscovered (-T/-F) drops them, and a discovered id
// equal to a registry row's id is dropped (the registry row wins).
func TestCatalogRowsHidesLocalModelsNotOnDisk(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"},
		{ID: "omlx/here", ProviderID: "omlx", ModelName: "here"},
		{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed"},
	}
	inv := &Snapshot{
		Providers: map[string]Status{"omlx": StatusOK},
		Entries: []Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x", ArtifactKnown: true},      // claude does not support mtplx
			{ProviderID: "ollama", ModelID: "omlx/here", Artifact: "dup", ArtifactKnown: true}, // id collision with a row
			{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok", ArtifactKnown: true},
		},
	}
	in := CatalogInput{Config: cfg, Agent: "claude", Models: models, Inventory: inv}
	rows := CatalogRows(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/here,ollama/ok" {
		t.Fatalf("rows = %s, want only the on-disk overlay and the discovered model", got)
	}
	if rows[0].Status != catalog.StatusOK || rows[0].Running || rows[0].Action() != catalog.ActionStart {
		t.Errorf("on-disk idle row = %+v, want status ok, startable", rows[0])
	}
	in.HideDiscovered = true
	if got := strings.Join(rowIDs(CatalogRows(in)), ","); got != "omlx/here" {
		t.Errorf("HideDiscovered rows = %s", got)
	}
}

// TestCatalogRowsHiddenRegistryIDShadowsDiscovered verifies a registry local model
// with no row still shadows a discovered artifact spelling the same id (the
// registry's model_name is a different artifact that is not on disk, while an
// unregistered artifact named like the id's suffix is). Listing that artifact
// under the registry id would make a -M pin launch it instead of reporting
// "not on disk", and the non-TUI path — which returns the registry model for
// an id — would launch the missing model_name.
func TestCatalogRowsHiddenRegistryIDShadowsDiscovered(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "ollama/foo", ProviderID: "ollama", ModelName: "bar"}}
	inv := &Snapshot{
		Providers: map[string]Status{"ollama": StatusOK},
		Entries: []Entry{
			{ProviderID: "ollama", ModelID: "ollama/foo", ModelName: "bar", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: config.DiscoveredModelID("ollama", "foo"), ModelName: "foo", Artifact: "foo", Running: true, ArtifactKnown: true},
		},
	}
	rows := CatalogRows(CatalogInput{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	if len(rows) != 0 {
		t.Fatalf("rows = %v, want none (the hidden registry id shadows the same-id discovered artifact)", rowIDs(rows))
	}
	if got := MissingReason(nil, inv, "ollama/foo"); !strings.Contains(got, "is not on disk") {
		t.Errorf("MissingReason = %q, want the not-on-disk reason for the pin", got)
	}
}

// TestCatalogRowsUnknownLocalStatus verifies a flaky probe never hides a model: a
// family whose discovery failed (ollama unreachable, ArtifactKnown false)
// still lists its registry models, as "unknown" and startable — hiding every
// pulled model whenever the daemon hiccups would empty the picker. A running
// row reads ok even though its artifact could not be resolved
// (mlx_lm_server), while a NON-running mlx_lm_server pairing is hidden:
// mlx_lm_server is "running only", wt can neither discover nor start it.
func TestCatalogRowsUnknownLocalStatus(t *testing.T) {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal})
	models := []config.Model{
		{ID: "ollama/unknown", ProviderID: "ollama", ModelName: "unknown", Location: config.LocationLocal},
		{ID: "mlx_lm_server/serving", ProviderID: "mlx_lm_server", ModelName: "serving", Location: config.LocationLocal},
		{ID: "mlx_lm_server/idle", ProviderID: "mlx_lm_server", ModelName: "idle", Location: config.LocationLocal},
	}
	inv := &Snapshot{
		Providers: map[string]Status{
			"ollama": StatusUnreachable, "mlx_lm_server": StatusOK,
		},
		Entries: []Entry{
			{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/serving", Registered: true, Running: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/idle", Registered: true},
		},
	}
	rows := CatalogRows(CatalogInput{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	byID := map[string]catalog.Row{}
	for _, r := range rows {
		byID[r.Model.ID] = r
	}
	if r, ok := byID["ollama/unknown"]; !ok || r.Status != catalog.StatusUnknown || r.Action() != catalog.ActionStart {
		t.Errorf("unreachable ollama row = %+v (present %v), want status unknown and startable (fail open)", r, ok)
	}
	if r, ok := byID["mlx_lm_server/serving"]; !ok || r.Status != catalog.StatusOK || !r.Running {
		t.Errorf("running mlx_lm_server row = %+v (present %v), want ok and running", r, ok)
	}
	if _, ok := byID["mlx_lm_server/idle"]; ok {
		t.Error("a non-running mlx_lm_server pairing must have no row")
	}
}

// TestCatalogRowsOverlayMatchedAgainstRealInventory runs a real inventory round
// (temp model directories, refused live probes — never a real server) and
// pins overlay matching end to end: a discovered artifact matching a registry
// entry's model_name takes that entry's id, family and tags — both for an id
// that spells the repo's "/" as "--" and for one shaped provider/model_name —
// an overlay whose artifact is missing gets no row, and an unmatched artifact
// becomes a discovered row under config.DiscoveredModelID with
// Source=discovered and no family. Stats and usage history are keyed on these ids,
// so a matching regression silently forks a model's history.
func TestCatalogRowsOverlayMatchedAgainstRealInventory(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	// An omlx model is a directory holding a config.json; mtplx's are bare.
	if err := MakeOmlxModelsForTest(omlxDir, "Qwen3.8-9B-4bit", "stray-model"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mtplxDir, "mlx-community--Qwen3.8-27B-4bit"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, ModelDir: omlxDir, Auth: config.AuthConfig{Type: "none", BaseURL: refused}},
			{ID: "mtplx", Location: config.LocationLocal, ModelDir: mtplxDir, Auth: config.AuthConfig{Type: "none", BaseURL: refused}},
		},
		Models: []config.Model{
			{ID: "omlx/Qwen3.8-9B-4bit", ProviderID: "omlx", ModelName: "Qwen3.8-9B-4bit", Family: "qwen", Tags: []string{"code"}},
			{ID: "mtplx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit", Family: "qwen", Tags: []string{"code"}},
			{ID: "omlx/absent-4bit", ProviderID: "omlx", ModelName: "absent-4bit", Family: "qwen", Tags: []string{"code"}},
		},
	}
	snap := Inventory(cfg)
	rows := CatalogRows(CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap})
	want := "omlx/Qwen3.8-9B-4bit,mtplx/mlx-community--Qwen3.8-27B-4bit," + config.DiscoveredModelID("omlx", "stray-model")
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	for _, r := range rows[:2] {
		if r.Discovered || r.Model.Family != "qwen" || !r.Model.HasTag("code") || r.Status != catalog.StatusOK {
			t.Errorf("matched overlay row %s = %+v, want the registry family/tags, status ok", r.Model.ID, r)
		}
	}
	d := rows[2]
	if !d.Discovered || d.Model.Source != config.SourceDiscovered || d.Model.Family != "" || d.Status != catalog.StatusNew {
		t.Errorf("unmatched row = %+v, want discovered, Source=discovered, no family, status new", d)
	}
}

// TestCatalogRowsListsStoppedRecAsStartRow runs a real inventory against a stopped
// rec (refused connection, never a real server) and verifies its registered
// rec/flint becomes a start row with unknown presence, neither blocked nor
// hidden: rec lists no artifacts, but unlike mlx_lm_server wt can start it.
// Hiding it would leave flint unstartable from the picker.
func TestCatalogRowsListsStoppedRecAsStartRow(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "rec", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "api_key", BaseURL: refused + "/v1", SecretRef: "k"}}},
		Models:    []config.Model{{ID: "rec/flint", ProviderID: "rec", ModelName: "flint"}},
	}
	snap := Inventory(cfg)
	rows := CatalogRows(CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap})
	if len(rows) != 1 || rows[0].Model.ID != "rec/flint" {
		t.Fatalf("rows = %v, want rec/flint listed", rowIDs(rows))
	}
	if r := rows[0]; r.Action() != catalog.ActionStart || r.Status != catalog.StatusUnknown || r.Running {
		t.Errorf("rec/flint row = %+v action %v, want a start row with status unknown", r, r.Action())
	}
}

// TestCatalogRowsHidesStoppedMlxLmServer runs a real inventory against a stopped
// mlx_lm_server and verifies its pairing is still not listed, and a row for
// one is still blocked: generalising the served-only probe to rec must not
// make mlx_lm_server startable, since wt has no engine for it.
func TestCatalogRowsHidesStoppedMlxLmServer(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: refused}}},
		Models:    []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p"}},
	}
	snap := Inventory(cfg)
	if rows := CatalogRows(CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap}); len(rows) != 0 {
		t.Errorf("rows = %v, want a stopped mlx_lm_server pairing hidden", rowIDs(rows))
	}
	r := catalog.Row{Location: config.LocationLocal, Status: catalog.StatusUnknown, Model: cfg.Models[0]}
	if r.Action() != catalog.ActionBlock {
		t.Errorf("mlx_lm_server row action = %v, want block", r.Action())
	}
}

// TestCatalogRowsWithoutInventoryKeepsLocalRowsStartable verifies the documented
// nil-Inventory contract: when no probe ran, a configured local row reads
// status ok, running false, and stays startable — the fail-open behavior the
// smoke-eligibility consumer relies on. A flip to absent/blocked here would
// make wt refuse models it never checked.
func TestCatalogRowsWithoutInventoryKeepsLocalRowsStartable(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"}}
	rows := CatalogRows(CatalogInput{Config: cfg, Agent: "claude", Models: models})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Status != catalog.StatusOK || rows[0].Running {
		t.Errorf("row = %+v, want status ok and running false", rows[0])
	}
	if rows[0].Action() != catalog.ActionStart {
		t.Errorf("action = %v, want catalog.ActionStart (fail open without a probe)", rows[0].Action())
	}
}

// TestCatalogRowsMarksDiscoveredModelsPassedViaModels verifies a discovered model
// handed in through Models (as the picker receives rows a caller already built)
// takes Discovered/catalog.StatusNew/Running from its non-registered inventory entry, so
// a running detected model resolves to launch and an idle one to start rather
// than reading as an unknown, startable row.
func TestCatalogRowsMarksDiscoveredModelsPassedViaModels(t *testing.T) {
	mk := func(id, name string) config.Model {
		return config.Model{ID: id, ProviderID: "ollama", ModelName: name, Location: config.LocationLocal, Source: config.SourceDiscovered}
	}
	snap := &Snapshot{Entries: []Entry{
		{ProviderID: "ollama", Artifact: "run:1", ModelID: "ollama/run:1", Running: true},
		{ProviderID: "ollama", Artifact: "idle:1", ModelID: "ollama/idle:1"},
	}}
	rows := CatalogRows(CatalogInput{Config: catalogTestCfg(), Models: []config.Model{mk("ollama/run:1", "run:1"), mk("ollama/idle:1", "idle:1")}, Inventory: snap})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if !r.Discovered || r.Status != catalog.StatusNew {
			t.Errorf("%s: Discovered=%v Status=%q, want discovered/new", r.Model.ID, r.Discovered, r.Status)
		}
	}
	if !rows[0].Running || rows[0].Action() != catalog.ActionLaunch {
		t.Errorf("running discovered row: Running=%v action=%v, want launch", rows[0].Running, rows[0].Action())
	}
	if rows[1].Running || rows[1].Action() != catalog.ActionStart {
		t.Errorf("idle discovered row: Running=%v action=%v, want start", rows[1].Running, rows[1].Action())
	}
}

// TestCatalogRowsMarksUnmappedCloudRows pins finding 3 of the #179 review: a cloud
// model whose provider has no LiteLLM mapping is in the catalog (configured
// means exposed) but sync never routes it, so its row is marked Unmapped, the
// mark the model server blocks the row on (internal/modelserver).
func TestCatalogRowsMarksUnmappedCloudRows(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "acme", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		},
	}
	mapped := config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x"}
	unmapped := config.Model{ID: "acme/m", ProviderID: "acme", ModelName: "m"}
	rows := CatalogRows(CatalogInput{Config: cfg, Models: []config.Model{mapped, unmapped}})
	if len(rows) != 2 || rows[0].Unmapped || !rows[1].Unmapped {
		t.Fatalf("rows = %+v, want only acme/m Unmapped", rows)
	}
}

// TestMissingReason verifies the message a pin of a hidden registry model
// gets (#179 Phase B): an overlay the probe confirmed is not on disk says so,
// a non-running mlx_lm_server pairing names the llmbench command that starts
// it (with its target and draft, read from the registry row), and everything
// that has a row — or that the probe could not vouch for — gets "" so the
// caller falls back to its generic wording. Without it, `wt -M <absent-id>`
// would say "not in the eligible list" and send the user hunting for a
// filter or agent problem instead of a download.
func TestMissingReason(t *testing.T) {
	snap := &Snapshot{Entries: []Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
		{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
		{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true},
		{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/run", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", ArtifactKnown: true},
	}}
	cases := map[string]string{
		"omlx/gone":         "omlx/gone is not on disk — pull or download it first",
		"mlx_lm_server/p":   "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server org/target --draft org/draft`",
		"omlx/here":         "",
		"mlx_lm_server/run": "",
		"ollama/unknown":    "",
		"omlx/disc":         "",
		"nope/x":            "",
	}
	cfg := &config.Config{Models: []config.Model{{
		ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "target+draft-draft",
		Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{Repo: "org/draft"},
	}}}
	for id, want := range cases {
		if got := MissingReason(cfg, snap, id); got != want {
			t.Errorf("MissingReason(%s) = %q, want %q", id, got, want)
		}
	}
	// With no config to read the pairing from, the command keeps its shape.
	if got := MissingReason(nil, snap, "mlx_lm_server/p"); !strings.HasSuffix(got, "`llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`") {
		t.Errorf("MissingReason without a config = %q, want the command with placeholders", got)
	}
	if got := MissingReason(cfg, nil, "omlx/gone"); got != "" {
		t.Errorf("MissingReason(nil snapshot) = %q, want empty (no probe ran)", got)
	}
}

// TestALoadingRowIsAStartRow pins #259 for every model list wt builds. A model
// omlx is still loading keeps Running — it occupies the pool and stays listed
// — but selecting it must start (join the load and wait), not launch: a launch
// hands an agent a model that cannot answer yet. The flag has to survive all
// three ways CatalogRows makes a local row: a registry model, a discovered entry,
// and a discovered model handed back in through Models.
func TestALoadingRowIsAStartRow(t *testing.T) {
	inv := &Snapshot{Entries: []Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true, Loading: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", Running: true, Loading: true},
		{ProviderID: "omlx", ModelID: "omlx/ready", Artifact: "ready", Running: true},
	}}
	registry := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"}}
	rows := CatalogRows(CatalogInput{Config: catalogTestCfg(), Agent: "claude", Models: registry, Inventory: inv})
	// Second pass: the rows' own models fed back in, as `wt start`'s picker does.
	var again []config.Model
	for _, r := range rows {
		again = append(again, r.Model)
	}
	for name, got := range map[string][]catalog.Row{"first build": rows, "rows fed back": CatalogRows(CatalogInput{Config: catalogTestCfg(), Models: again, Inventory: inv})} {
		if ids := strings.Join(rowIDs(got), ","); ids != "omlx/a,omlx/disc,omlx/ready" {
			t.Fatalf("%s: rows = %s", name, ids)
		}
		for _, r := range got[:2] {
			if !r.Running || !r.Loading || r.Ready() || r.Action() != catalog.ActionStart {
				t.Errorf("%s: %s Running=%v Loading=%v Ready=%v action=%v, want a running, loading start row", name, r.Model.ID, r.Running, r.Loading, r.Ready(), r.Action())
			}
		}
		if r := got[2]; r.Loading || !r.Ready() || r.Action() != catalog.ActionLaunch {
			t.Errorf("%s: %s Loading=%v Ready=%v action=%v, want a loaded launch row", name, r.Model.ID, r.Loading, r.Ready(), r.Action())
		}
	}
}

// TestCatalogRowsWithoutInventoryEqualsModelRows pins that CatalogRows with a
// nil Inventory is exactly catalog.ModelRows: the standalone picker builds its
// rows with ModelRows (no admin import), every other caller through
// CatalogRows, and the two must give the same rows for cloud, mapped,
// unmapped, native and local models alike. A drift would make the picker and
// `wt smoke` disagree about a model's location or whether it can launch.
func TestCatalogRowsWithoutInventoryEqualsModelRows(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		{ID: "acme", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		{ID: "omlx", Location: config.LocationLocal},
	}}
	models := []config.Model{
		{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x"},
		{ID: "acme/m", ProviderID: "acme", ModelName: "m"},
		{ID: "acme/n", ProviderID: "acme", ModelName: "n", Native: true},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"},
	}
	got := CatalogRows(CatalogInput{Config: cfg, Agent: "claude", Models: models, HideDiscovered: true})
	want := catalog.ModelRows(cfg, models)
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Model.ID != want[i].Model.ID || got[i].Location != want[i].Location || got[i].Status != want[i].Status ||
			got[i].Running != want[i].Running || got[i].Unmapped != want[i].Unmapped || got[i].Discovered != want[i].Discovered {
			t.Errorf("row %d: CatalogRows %+v != ModelRows %+v", i, got[i], want[i])
		}
	}
}
