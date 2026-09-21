// Tests for the shared model-selector row rules: which models become rows,
// each row's live presence status, and what selecting a row can do. These
// moved from internal/tui when the rules were extracted, so the picker and
// the non-TUI launch path can no longer disagree about a model's state.
package catalog

import (
	"errors"
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
	cfg.SetExposedForTest(map[string]config.ExposureEntry{"openrouter/cheap": {Exposed: true, Ready: true}})
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
	if !rows[0].Exposed || rows[0].Location != config.LocationCloud || rows[0].Status != StatusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].Running || rows[1].Status != StatusOK || rows[1].Discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.Discovered || d.Status != StatusNew || d.Exposed || d.Running || d.Model.ModelName != "disc" || d.Location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestBuildMarksAbsentAndRespectsAgentAndFilters verifies: a configured local
// model with no artifact reads "absent"; discovered models from a provider
// the agent does not support are skipped (agent supported_providers stays a
// hard constraint); HideDiscovered (-T/-F) drops discovered rows; a
// discovered id equal to an existing row id is dropped (registry row wins).
func TestBuildMarksAbsentAndRespectsAgentAndFilters(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"}}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x"},      // claude does not support mtplx
		{ProviderID: "ollama", ModelID: "omlx/gone", Artifact: "dup"}, // id collision with a row
		{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok"},
	}}
	in := Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}
	rows := Build(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/gone,ollama/ok" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].Status != StatusAbsent {
		t.Errorf("Status = %q, want absent", rows[0].Status)
	}
	in.HideDiscovered = true
	if got := strings.Join(rowIDs(Build(in)), ","); got != "omlx/gone" {
		t.Errorf("HideDiscovered rows = %s", got)
	}
}

// TestBuildNativeModelIsExposedWithoutFlag pins that a native model
// (Anthropic-direct, provider auth.type "native") shows EXPOSED without any
// modelman.toml flag, matching modelman's own EXPOSED column, which reports
// native rows as exposed unconditionally. A regression here would make wt's
// picker disagree with modelman for native rows.
func TestBuildNativeModelIsExposedWithoutFlag(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "openrouter/native", ProviderID: "openrouter", ModelName: "native", Native: true}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models})
	if len(rows) != 1 || !rows[0].Exposed {
		t.Fatalf("native row = %+v, want exposed=true with no flag set", rows)
	}
	if cfg.ExposedFlag("openrouter/native") {
		t.Fatal("test premise broken: flag must be unset")
	}
}

// TestBuildUnknownLocalStatus verifies the three-way artifact rule: a probe
// that answered and found nothing reads "absent" (and the row is blocked: the
// engine would wait out its warmup on a model that is not there), a probe that
// could not tell reads "unknown" (and stays startable — a transient daemon
// hiccup must not make the picker refuse to start a model that is actually
// pulled), and a row serving right now never reads absent even when its
// artifact could not be resolved (mlx_lm_server, whose target+draft pairing is
// not discoverable).
func TestBuildUnknownLocalStatus(t *testing.T) {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal})
	models := []config.Model{
		{ID: "ollama/unknown", ProviderID: "ollama", ModelName: "unknown", Location: config.LocationLocal},
		{ID: "omlx/nope", ProviderID: "omlx", ModelName: "nope", Location: config.LocationLocal},
		{ID: "mlx_lm_server/serving", ProviderID: "mlx_lm_server", ModelName: "serving", Location: config.LocationLocal},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "omlx": localmodels.StatusOK,
		},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
			{ProviderID: "omlx", ModelID: "omlx/nope", Registered: true, ArtifactKnown: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/serving", Registered: true, Running: true},
		},
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.Model.ID] = r
	}
	if got := byID["ollama/unknown"].Status; got != StatusUnknown {
		t.Errorf("unreachable ollama status = %q, want unknown", got)
	}
	if got := byID["ollama/unknown"].Action(); got != ActionStart {
		t.Errorf("unreachable ollama action = %v, want ActionStart (fail open)", got)
	}
	if got := byID["omlx/nope"].Status; got != StatusAbsent {
		t.Errorf("answered omlx status = %q, want absent", got)
	}
	if got := byID["omlx/nope"].Action(); got != ActionBlock {
		t.Errorf("an absent non-running omlx row must be blocked, got action %v", got)
	}
	if got := byID["mlx_lm_server/serving"].Status; got != StatusOK {
		t.Errorf("running mlx_lm_server status = %q, want ok", got)
	}
}

// TestBuildLocalModelMissingFromSnapshot verifies a local registry model with
// no inventory entry at all (an unresolvable location, or a family wt has no
// probe for) reads "unknown" rather than "absent": nothing was discovered
// about it, so calling it missing would be a guess.
func TestBuildLocalModelMissingFromSnapshot(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed", Location: config.LocationLocal}}
	inv := &localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	if rows[0].Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown", rows[0].Status)
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
// discovered row and an unknown-presence row; an absent row and a provider
// with no start engine (mlx_lm_server) are blocked. Getting this wrong either
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
		{"absent omlx", local("omlx", StatusAbsent, false, false), ActionBlock},
		{"absent ollama", local("ollama", StatusAbsent, false, false), ActionBlock},
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
// what to do: pull/download for an absent model, `modelman start <id>` for a
// provider wt cannot start, and "" for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	absent := Row{Location: config.LocationLocal, Status: StatusAbsent, Model: config.Model{ID: "omlx/a", ProviderID: "omlx"}}
	if h := absent.BlockReason(); !strings.Contains(h, "omlx/a") || !strings.Contains(h, "not on disk") {
		t.Errorf("absent reason = %q", h)
	}
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
