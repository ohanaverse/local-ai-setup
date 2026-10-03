// Tests for the shared model-selector row rules: which models become rows,
// each row's live presence status, and what selecting a row can do. These
// moved from internal/tui when the rules were extracted, so the picker and
// the non-TUI launch path can no longer disagree about a model's state.
package catalog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

func rowIDs(rows []Row) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Model.ID
	}
	return ids
}

// TestBuildIncludesConfiguredAndDiscoveredLocal verifies the three row
// sources: a configured cloud model, a configured local model (running or
// not), and a discovered on-disk model with no registry entry — the union
// the selector is meant to show.
func TestBuildIncludesConfiguredAndDiscoveredLocal(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "openrouter/cheap", ProviderID: "openrouter", ModelName: "cheap"},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"},
	}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc"},
	}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})

	if got := strings.Join(rowIDs(rows), ","); got != "openrouter/cheap,omlx/a,omlx/disc" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].Location != config.LocationCloud || rows[0].Status != StatusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].Running || rows[1].Status != StatusOK || rows[1].Discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.Discovered || d.Status != StatusNew || d.Running || d.Model.ModelName != "disc" || d.Location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestBuildHidesLocalModelsNotOnDisk verifies #179 Phase B's row rule: a
// registry local model gets a row only from its inventory entry. An overlay
// the probe confirmed is not on disk, and one with no inventory entry at all
// (e.g. its location could not be resolved), both get no row — the picker
// lists what exists, not what is configured. Agent supported_providers stays a hard constraint
// for discovered rows, HideDiscovered (-T/-F) drops them, and a discovered id
// equal to a registry row's id is dropped (the registry row wins).
func TestBuildHidesLocalModelsNotOnDisk(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"},
		{ID: "omlx/here", ProviderID: "omlx", ModelName: "here"},
		{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed"},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x", ArtifactKnown: true},      // claude does not support mtplx
			{ProviderID: "ollama", ModelID: "omlx/here", Artifact: "dup", ArtifactKnown: true}, // id collision with a row
			{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok", ArtifactKnown: true},
		},
	}
	in := Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}
	rows := Build(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/here,ollama/ok" {
		t.Fatalf("rows = %s, want only the on-disk overlay and the discovered model", got)
	}
	if rows[0].Status != StatusOK || rows[0].Running || rows[0].Action() != ActionStart {
		t.Errorf("on-disk idle row = %+v, want status ok, startable", rows[0])
	}
	in.HideDiscovered = true
	if got := strings.Join(rowIDs(Build(in)), ","); got != "omlx/here" {
		t.Errorf("HideDiscovered rows = %s", got)
	}
}

// TestBuildUnknownLocalStatus verifies a flaky probe never hides a model: a
// family whose discovery failed (ollama unreachable, ArtifactKnown false)
// still lists its registry models, as "unknown" and startable — hiding every
// pulled model whenever the daemon hiccups would empty the picker. A running
// row reads ok even though its artifact could not be resolved
// (mlx_lm_server), while a NON-running mlx_lm_server pairing is hidden:
// mlx_lm_server is "running only", wt can neither discover nor start it.
func TestBuildUnknownLocalStatus(t *testing.T) {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal})
	models := []config.Model{
		{ID: "ollama/unknown", ProviderID: "ollama", ModelName: "unknown", Location: config.LocationLocal},
		{ID: "mlx_lm_server/serving", ProviderID: "mlx_lm_server", ModelName: "serving", Location: config.LocationLocal},
		{ID: "mlx_lm_server/idle", ProviderID: "mlx_lm_server", ModelName: "idle", Location: config.LocationLocal},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "mlx_lm_server": localmodels.StatusOK,
		},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/serving", Registered: true, Running: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/idle", Registered: true},
		},
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.Model.ID] = r
	}
	if r, ok := byID["ollama/unknown"]; !ok || r.Status != StatusUnknown || r.Action() != ActionStart {
		t.Errorf("unreachable ollama row = %+v (present %v), want status unknown and startable (fail open)", r, ok)
	}
	if r, ok := byID["mlx_lm_server/serving"]; !ok || r.Status != StatusOK || !r.Running {
		t.Errorf("running mlx_lm_server row = %+v (present %v), want ok and running", r, ok)
	}
	if _, ok := byID["mlx_lm_server/idle"]; ok {
		t.Error("a non-running mlx_lm_server pairing must have no row")
	}
}

// TestBuildOverlayMatchedAgainstRealInventory runs a real inventory round
// (temp model directories, refused live probes — never a real server) and
// pins overlay matching end to end: a discovered artifact matching a registry
// entry's model_name takes that entry's id, family and tags — both for an id
// that spells the repo's "/" as "--" and for one shaped provider/model_name —
// an overlay whose artifact is missing gets no row, and an unmatched artifact
// becomes a discovered row under config.DiscoveredModelID with
// Source=discovered and no family. Stats and rotation are keyed on these ids,
// so a matching regression silently forks a model's history.
func TestBuildOverlayMatchedAgainstRealInventory(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	for _, d := range []string{
		filepath.Join(omlxDir, "Qwen3.8-9B-4bit"),
		filepath.Join(omlxDir, "stray-model"),
		filepath.Join(mtplxDir, "mlx-community--Qwen3.8-27B-4bit"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
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
	snap := localmodels.Inventory(cfg)
	rows := Build(Input{Config: cfg, Models: cfg.Models, Inventory: &snap})
	want := "omlx/Qwen3.8-9B-4bit,mtplx/mlx-community--Qwen3.8-27B-4bit," + config.DiscoveredModelID("omlx", "stray-model")
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	for _, r := range rows[:2] {
		if r.Discovered || r.Model.Family != "qwen" || !r.Model.HasTag("code") || r.Status != StatusOK {
			t.Errorf("matched overlay row %s = %+v, want the registry family/tags, status ok", r.Model.ID, r)
		}
	}
	d := rows[2]
	if !d.Discovered || d.Model.Source != config.SourceDiscovered || d.Model.Family != "" || d.Status != StatusNew {
		t.Errorf("unmatched row = %+v, want discovered, Source=discovered, no family, status new", d)
	}
}

// TestBuildWithoutInventoryKeepsLocalRowsStartable verifies the documented
// nil-Inventory contract: when no probe ran, a configured local row reads
// status ok, running false, and stays startable — the fail-open behavior the
// smoke-eligibility consumer relies on. A flip to absent/blocked here would
// make wt refuse models it never checked.
func TestBuildWithoutInventoryKeepsLocalRowsStartable(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Status != StatusOK || rows[0].Running {
		t.Errorf("row = %+v, want status ok and running false", rows[0])
	}
	if rows[0].Action() != ActionStart {
		t.Errorf("action = %v, want ActionStart (fail open without a probe)", rows[0].Action())
	}
}

// TestRowActionRules verifies what selecting a row does per row kind: cloud
// and running local rows launch; a non-running local row of a provider wt can
// start (ollama, omlx, mtplx) starts — including a pulled ollama model, a
// discovered row and an unknown-presence row; a provider with no start
// engine (mlx_lm_server) is blocked. Getting this wrong either
// launches a dead model or hides one wt could have started.
func TestRowActionRules(t *testing.T) {
	local := func(provider string, status Status, running, discovered bool) Row {
		return Row{Location: config.LocationLocal, Status: status, Running: running, Discovered: discovered, Model: config.Model{ID: provider + "/x", ProviderID: provider}}
	}
	cases := []struct {
		name string
		row  Row
		want Action
	}{
		{"cloud", Row{Location: config.LocationCloud}, ActionLaunch},
		{"running omlx", local("omlx", StatusOK, true, false), ActionLaunch},
		{"idle omlx on disk", local("omlx", StatusOK, false, false), ActionStart},
		{"idle mtplx discovered", local("mtplx", StatusNew, false, true), ActionStart},
		{"idle ollama pulled", local("ollama", StatusOK, false, false), ActionStart},
		{"idle ollama unknown presence", local("ollama", StatusUnknown, false, false), ActionStart},
		{"mlx_lm_server has no start engine", local("mlx_lm_server", StatusOK, false, false), ActionBlock},
		{"omlx-6bit shares omlx's engine", local("omlx-6bit", StatusOK, false, false), ActionStart},
	}
	for _, tc := range cases {
		if got := tc.row.Action(); got != tc.want {
			t.Errorf("%s: action = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBlockReasonNamesTheFix verifies the status text for a blocked row names
// what to do — `modelman start <id>` for a provider wt cannot start — and is
// "" for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server"}}
	if h := mlx.BlockReason(); !strings.Contains(h, "modelman start mlx_lm_server/p") {
		t.Errorf("mlx_lm_server reason = %q", h)
	}
	if h := (Row{Location: config.LocationCloud}).BlockReason(); h != "" {
		t.Errorf("cloud reason = %q, want empty", h)
	}
	if h := (Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ProviderID: "omlx"}}).BlockReason(); h != "" {
		t.Errorf("startable row reason = %q, want empty", h)
	}
}

// TestFindReportsDiscoveredRows verifies Find looks a row up among ALL rows,
// discovered ones included — the fix that lets `wt -M <discovered-id>` work.
// A registry-only lookup would send users to a registry edit they do not need.
func TestFindReportsDiscoveredRows(t *testing.T) {
	rows := []Row{
		{Model: config.Model{ID: "omlx/a", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusOK},
		{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusNew, Discovered: true},
	}
	if r, ok := Find(rows, "omlx/disc"); !ok || !r.Discovered {
		t.Errorf("Find(omlx/disc) = (%+v, %v), want the discovered row", r, ok)
	}
	if r, ok := Find(rows, "omlx/a"); !ok || r.Discovered {
		t.Errorf("Find(omlx/a) = (%+v, %v), want the registered row", r, ok)
	}
	if _, ok := Find(rows, "omlx/nope"); ok {
		t.Error("Find(omlx/nope) reported a hit, want miss")
	}
}

// TestRefusedByRoute verifies the one route-refusal rule: only a discovered
// row whose route resolved AND goes through LiteLLM (routed or forced) is
// refused. Registered rows are never refused, and a route error is not a
// refusal (the picker reports it on Enter instead). The picker, the non-TUI
// -M pin and `wt smoke` all call this, so a change here moves all three.
func TestRefusedByRoute(t *testing.T) {
	direct, viaProxy, forced := config.Route{}, config.Route{Litellm: true}, config.Route{Forced: true}
	cases := []struct {
		name       string
		discovered bool
		route      config.Route
		err        error
		want       bool
	}{
		{"discovered via litellm", true, viaProxy, nil, true},
		{"discovered forced", true, forced, nil, true},
		{"discovered direct", true, direct, nil, false},
		{"discovered with route error", true, viaProxy, errors.New("no route"), false},
		{"registered via litellm", false, viaProxy, nil, false},
	}
	for _, c := range cases {
		if got := (Row{Discovered: c.discovered}).RefusedByRoute(c.route, c.err); got != c.want {
			t.Errorf("%s: RefusedByRoute = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBuildMarksDiscoveredModelsPassedViaModels verifies a discovered model
// handed in through Models (as the picker receives rows a caller already built)
// takes Discovered/StatusNew/Running from its non-registered inventory entry, so
// a running detected model resolves to launch and an idle one to start rather
// than reading as an unknown, startable row.
func TestBuildMarksDiscoveredModelsPassedViaModels(t *testing.T) {
	mk := func(id, name string) config.Model {
		return config.Model{ID: id, ProviderID: "ollama", ModelName: name, Location: config.LocationLocal, Source: config.SourceDiscovered}
	}
	snap := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "ollama", Artifact: "run:1", ModelID: "ollama/run:1", Running: true},
		{ProviderID: "ollama", Artifact: "idle:1", ModelID: "ollama/idle:1"},
	}}
	rows := Build(Input{Config: catalogTestCfg(), Models: []config.Model{mk("ollama/run:1", "run:1"), mk("ollama/idle:1", "idle:1")}, Inventory: snap})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if !r.Discovered || r.Status != StatusNew {
			t.Errorf("%s: Discovered=%v Status=%q, want discovered/new", r.Model.ID, r.Discovered, r.Status)
		}
	}
	if !rows[0].Running || rows[0].Action() != ActionLaunch {
		t.Errorf("running discovered row: Running=%v action=%v, want launch", rows[0].Running, rows[0].Action())
	}
	if rows[1].Running || rows[1].Action() != ActionStart {
		t.Errorf("idle discovered row: Running=%v action=%v, want start", rows[1].Running, rows[1].Action())
	}
}

// TestBuildMarksUnmappedCloudRows pins finding 3 of the #179 review: a cloud
// model whose provider has no LiteLLM mapping is in the catalog (configured
// means exposed) but sync never routes it, so its row is marked Unmapped and
// refused through the proxy — with LiteLLM off it stays launchable.
func TestBuildMarksUnmappedCloudRows(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "acme", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		},
	}
	mapped := config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x"}
	unmapped := config.Model{ID: "acme/m", ProviderID: "acme", ModelName: "m"}
	rows := Build(Input{Config: cfg, Models: []config.Model{mapped, unmapped}})
	if len(rows) != 2 || rows[0].Unmapped || !rows[1].Unmapped {
		t.Fatalf("rows = %+v, want only acme/m Unmapped", rows)
	}
	viaProxy := config.Route{Litellm: true}
	if !rows[1].RefusedByRoute(viaProxy, nil) || rows[1].RefusedByRoute(config.Route{}, nil) {
		t.Fatal("unmapped row: want refused via LiteLLM, launchable direct")
	}
	if r := rows[1].RouteRefusal(viaProxy, nil); !strings.Contains(r, "acme/m") || !strings.Contains(r, "wt litellm off") {
		t.Fatalf("RouteRefusal = %q", r)
	}
	if rows[0].RouteRefusal(viaProxy, nil) != "" {
		t.Fatal("mapped cloud row refused")
	}
}

// TestMissingReason verifies the message a pin of a hidden registry model
// gets (#179 Phase B): an overlay the probe confirmed is not on disk says so,
// a non-running mlx_lm_server pairing names `modelman start`, and everything
// that has a row — or that the probe could not vouch for — gets "" so the
// caller falls back to its generic wording. Without it, `wt -M <absent-id>`
// would say "not in the eligible list" and send the user hunting for a
// filter or agent problem instead of a download.
func TestMissingReason(t *testing.T) {
	snap := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
		{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
		{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true},
		{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/run", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", ArtifactKnown: true},
	}}
	cases := map[string]string{
		"omlx/gone":         "omlx/gone is not on disk — pull or download it first",
		"mlx_lm_server/p":   "local model \"mlx_lm_server/p\" is not running — start it with `modelman start mlx_lm_server/p`",
		"omlx/here":         "",
		"mlx_lm_server/run": "",
		"ollama/unknown":    "",
		"omlx/disc":         "",
		"nope/x":            "",
	}
	for id, want := range cases {
		if got := MissingReason(snap, id); got != want {
			t.Errorf("MissingReason(%s) = %q, want %q", id, got, want)
		}
	}
	if got := MissingReason(nil, "omlx/gone"); got != "" {
		t.Errorf("MissingReason(nil snapshot) = %q, want empty (no probe ran)", got)
	}
}
