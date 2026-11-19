package litellm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestSyncInputsRecheckIsTheDesiredLocalSet pins SyncInputs' two probes: one
// for the inputs it returns, and one more only when the under-the-lock
// Recheck runs, which answers the desired local ids of that fresh probe. The
// probe is a parameter (cmd/wt-session passes its stubbable probeInventory
// seam), so a Recheck that kept the first snapshot, or probed eagerly, would
// lose a start landing between the probe and the config.yaml lock.
func TestSyncInputsRecheckIsTheDesiredLocalSet(t *testing.T) {
	calls := 0
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{{ProviderID: "ollama", ModelID: "ollama/q", ModelName: "q", Artifact: "q", ArtifactKnown: true, Running: true}}}
	probe := func(*config.Config) localmodels.Snapshot { calls++; return snap }
	desired, o, _ := SyncInputs(&config.Config{}, probe)
	// one probe for the inputs, one more only when Recheck runs
	if calls != 1 {
		t.Fatalf("probes = %d, want 1 before Recheck", calls)
	}
	if got := o.Recheck(); !slices.Equal(got, []string{"ollama/q"}) || calls != 2 {
		t.Fatalf("Recheck = %v after %d probes, want [ollama/q] after 2", got, calls)
	}
	if len(desired) != 1 || desired[0].ID != "ollama/q" {
		t.Fatalf("desired = %+v, want the running ollama/q", desired)
	}
}

// TestDesiredLocalIDsOllamaFollowsPulled pins #179's rule for which local
// models sync routes: any running one, plus an ollama model that is pulled
// whether or not it is loaded — registered or discovered (Phase B: a pulled
// model with no overlay is routed under its discovered id). ollama lazy-loads
// on request and unloads idle models, so keying ollama on "loaded" made a
// flag-only start get no route and made every sync after an idle unload drop
// a working route. "Pulled" must mean the probe actually saw the artifact
// (ArtifactKnown), and it must not widen to single-model families, which
// serve nothing until started.
func TestDesiredLocalIDsOllamaFollowsPulled(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK, "mtplx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/pulled:1", ModelName: "pulled:1", Artifact: "pulled:1", ArtifactKnown: true, Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/unknown:1", ModelName: "unknown:1", Artifact: "unknown:1", ArtifactKnown: false, Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/notpulled:1", ModelName: "notpulled:1", Artifact: "", ArtifactKnown: true, Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/discovered:1", ModelName: "discovered:1", Artifact: "discovered:1", ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/idle", ModelName: "idle", Artifact: "idle", ArtifactKnown: true, Registered: true},
			{ProviderID: "mtplx", ModelID: "mtplx/idle", ModelName: "idle", Artifact: "idle", ArtifactKnown: true, Registered: true},
			{ProviderID: "mtplx", ModelID: "mtplx/live", ModelName: "live", Artifact: "live", ArtifactKnown: true, Registered: true, Running: true},
			{ProviderID: "ollama", ModelID: "ollama/loaded:1", ModelName: "loaded:1", Registered: true, Running: true},
		},
	}
	want := []string{"ollama/pulled:1", "ollama/discovered:1", "mtplx/live", "ollama/loaded:1"}
	if got := desiredLocalIDs(&config.Config{}, snap); !slices.Equal(got, want) {
		t.Fatalf("desiredLocalIDs = %v, want %v", got, want)
	}
}

// TestDesiredLocalIDsPulledNeedsTrustedProbe pins that a pulled ollama model
// counts as desired only when ollama's probe is fully OK. When /api/tags
// answered but /api/ps was refused (the daemon died between the probes), the
// family is Down: its routes are stale and sync must remove them. Counting the
// pulled ids there would keep — or re-add — routes to a server that is not
// listening, so every request through LiteLLM would fail.
func TestDesiredLocalIDsPulledNeedsTrustedProbe(t *testing.T) {
	pulled := localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/pulled:1", ModelName: "pulled:1", Artifact: "pulled:1", ArtifactKnown: true, Registered: true}
	cases := []struct {
		name string
		snap localmodels.Snapshot
		want []string
	}{
		{"partial and down", localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"ollama": localmodels.StatusPartial},
			Down:      map[string]bool{"ollama": true},
			Entries:   []localmodels.Entry{pulled},
		}, nil},
		{"partial, not down", localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"ollama": localmodels.StatusPartial},
			Entries:   []localmodels.Entry{pulled},
		}, nil},
		{"ok", localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
			Entries:   []localmodels.Entry{pulled},
		}, []string{"ollama/pulled:1"}},
	}
	for _, c := range cases {
		if got := desiredLocalIDs(&config.Config{}, c.snap); !slices.Equal(got, c.want) {
			t.Errorf("%s: desiredLocalIDs = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSyncRoutesMlxLMServerByProbe pins that `wt litellm sync` owns the
// mlx_lm_server route lifecycle (#179): wt has no start backend for it
// (`llmbench provider isolate` starts a pairing) and nothing else writes its
// route, so sync is the only route write.
// An answering server makes its registered pairing desired and NOT untouched,
// with no warning (a started pairing gets a route); a refused one makes the
// family Down, so the pairing is neither desired nor untouched and its stale
// route is removed. The snapshot comes from a real Inventory round against
// local httptest servers (never a real provider), because the bug was in the
// probe's status, which a hand-built snapshot would assume away.
func TestSyncRoutesMlxLMServerByProbe(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"/some/target/path"}]}`))
	}))
	defer up.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()

	cfgFor := func(base string) *config.Config {
		return &config.Config{
			Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: base + "/v1"}}},
			Models:    []config.Model{{ID: "mlx_lm_server/pair", ProviderID: "mlx_lm_server", ModelName: "pair", Location: config.LocationLocal}},
		}
	}
	routed := map[string]bool{"mlx_lm_server/pair": true}

	cfg := cfgFor(up.URL)
	snap := localmodels.Inventory(cfg)
	if got := desiredLocalIDs(cfg, snap); !slices.Equal(got, []string{"mlx_lm_server/pair"}) {
		t.Errorf("answering: desiredLocalIDs = %v, want the running pairing", got)
	}
	untouched, warns := syncUntouchedAndWarnings(cfg, snap, routed)
	if len(untouched) != 0 || len(warns) != 0 {
		t.Errorf("answering: untouched = %v, warnings = %v; want neither", untouched, warns)
	}

	cfg = cfgFor(goneURL)
	snap = localmodels.Inventory(cfg)
	if got := desiredLocalIDs(cfg, snap); len(got) != 0 {
		t.Errorf("refused: desiredLocalIDs = %v, want none", got)
	}
	untouched, warns = syncUntouchedAndWarnings(cfg, snap, routed)
	if len(untouched) != 0 {
		t.Errorf("refused: untouched = %v, want none (the stale route must be removable)", untouched)
	}
	want := []string{`provider "mlx_lm_server" refused the probe connection: nothing is listening there, so its local routes are treated as stale`}
	if !slices.Equal(warns, want) {
		t.Errorf("refused: warnings = %v, want %v", warns, want)
	}
}

// TestSyncRoutesMlxLMServerUnattributableAnswerLeavesRoutes pins that with two
// or more registered mlx_lm_server pairings and a /v1/models answer that names
// none of them, sync leaves every pairing's route alone with a warning saying
// why: wt cannot tell which pairing is serving.
func TestSyncRoutesMlxLMServerUnattributableAnswerLeavesRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, served, nameA, nameB string
	}{
		// The pairings' "+draft-" names match none of the served ids
		// (mlx_lm.server lists HF repo ids, never the pairing name). wt cannot
		// tell which pairing is serving; treating the family as OK removed the
		// serving pairing's working route on every sync, silently.
		{"ambiguous", `{"data":[{"id":"mlx-community/Qwen3.8-27B-4bit"},{"id":"mlx-community/Qwen3.8-0.6B-4bit"}]}`,
			"mlx-community/Qwen3.8-27B-4bit+draft-mlx-community/Qwen3.8-0.6B-4bit",
			"mlx-community/Qwen3.8-27B-4bit+draft-mlx-community/Qwen3.8-1.7B-4bit"},
		// The same guarantee for an EMPTY /v1/models body: with two registered
		// pairings, "answered and listed nothing" is no more attributable to a
		// pairing than a foreign id is, so sync must leave both routes alone
		// and say so. Reporting OK there removed every registered pairing's
		// route silently — a real server lists the pairing it loaded, so the
		// only ways to get an empty list are a foreign listener on the port or
		// a server still starting up.
		{"empty answer", `{"data":[]}`, "pair-a", "pair-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.served))
			}))
			defer up.Close()
			cfg := &config.Config{
				Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: up.URL + "/v1"}}},
				Models: []config.Model{
					{ID: "mlx_lm_server/a", ProviderID: "mlx_lm_server", ModelName: tc.nameA, Location: config.LocationLocal},
					{ID: "mlx_lm_server/b", ProviderID: "mlx_lm_server", ModelName: tc.nameB, Location: config.LocationLocal},
				},
			}
			snap := localmodels.Inventory(cfg)
			if got := desiredLocalIDs(cfg, snap); len(got) != 0 {
				t.Errorf("desiredLocalIDs = %v, want none", got)
			}
			untouched, warns := syncUntouchedAndWarnings(cfg, snap, map[string]bool{"mlx_lm_server/a": true})
			if !slices.Equal(untouched, []string{"mlx_lm_server/a", "mlx_lm_server/b"}) {
				t.Errorf("untouched = %v, want both pairings (routes kept)", untouched)
			}
			want := []string{`provider "mlx_lm_server" answered, but wt cannot tell which of its registered models it is serving (status "partial"); its model routes were left unchanged`}
			if !slices.Equal(warns, want) {
				t.Errorf("warnings = %v, want %v", warns, want)
			}
		})
	}
}

// TestUntrustedFamilies pins which families sync freezes: every family whose
// probe RAN and came back neither OK nor refused (Down) — partial or
// unreachable — so their discovered routes, which carry no registry id the
// per-id Untouched list could name, are left exactly as they are. A family
// with no key in Snapshot.Providers was never probed (the registry has no
// local provider row or local model for it): nothing is in doubt about it, so
// it is NOT frozen — freezing it kept its leftover routes forever, since no
// later probe could ever vouch for a family the inventory never asks. The
// exception — an unprobed family the registry references unresolvably — is
// pinned end to end by TestLitellmSyncKeepsRoutesOfRegistryGapFamily.
func TestUntrustedFamilies(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable,
		},
		Down: map[string]bool{"mtplx": true},
	}
	if got := untrustedFamilies(&config.Config{}, snap); !slices.Equal(got, []string{"omlx"}) {
		t.Fatalf("untrustedFamilies = %v, want [omlx] (mlx_lm_server was never probed, mtplx refused)", got)
	}
	snap.Providers["mlx_lm_server"] = localmodels.StatusUnreachable
	if got := untrustedFamilies(&config.Config{}, snap); !slices.Equal(got, []string{"mlx_lm_server", "omlx"}) {
		t.Fatalf("untrustedFamilies = %v, want [mlx_lm_server omlx] once mlx_lm_server's probe ran and failed", got)
	}
}

// TestSyncProbeWarningIgnoresHandWrittenRows pins that the discovered-only
// family warning is about wt's routes alone: an untrusted family whose only
// config.yaml row is hand-written (unmarked) froze nothing of wt's — sync
// never touches that row whatever the probe says — so it stays silent, while
// a family with a marked row is named. Warning for the hand-written row would
// nag on every sync about a provider the user routes by hand and never runs.
func TestSyncProbeWarningIgnoresHandWrittenRows(t *testing.T) {
	cfg := litellmTestConfig()
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable, "mtplx": localmodels.StatusUnreachable},
	}
	routed := map[string]bool{"mtplx/hand-written": false, "omlx/stray-model": true}
	untouched, warns := syncUntouchedAndWarnings(cfg, snap, routed)
	want := []string{`provider "omlx" probe did not succeed (status "unreachable"); its model routes were left unchanged`}
	if len(untouched) != 0 || !slices.Equal(warns, want) {
		t.Fatalf("untouched = %v, warnings = %q; want none and only %q", untouched, warns, want)
	}
}

// TestSyncProbeWarningCarriesTheProbeError pins that the probe warning carries
// the probe's error when it has one: the status says only "partial", but the
// error names the repair — a mixed omlx pool whose status endpoint wants the
// server's key is actionable ("set auth.secret_ref ...") in a way "probe did
// not succeed" is not. Snapshots without an error keep the plain wording, so
// hand-built ones read as they always did.
func TestSyncProbeWarningCarriesTheProbeError(t *testing.T) {
	cfg := litellmTestConfig()
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial},
		ProbeFailures: map[string]error{
			"omlx": fmt.Errorf("omlx has 1 of 2 models loaded and would not say which (set auth.secret_ref on the registry's omlx provider if the server has an API key): GET http://localhost:8000/v1/models/status: status 401"),
		},
	}
	routed := map[string]bool{"omlx/stray-model": true}
	untouched, warns := syncUntouchedAndWarnings(cfg, snap, routed)
	want := []string{`provider "omlx" probe did not succeed (status "partial"): omlx has 1 of 2 models loaded and would not say which (set auth.secret_ref on the registry's omlx provider if the server has an API key): GET http://localhost:8000/v1/models/status: status 401; its model routes were left unchanged`}
	if len(untouched) != 0 || !slices.Equal(warns, want) {
		t.Fatalf("untouched = %v, warnings = %q; want none and only the warning with the probe error", untouched, warns)
	}
}

// TestMissingProviderWarningsOrderAndTheLineAlreadySaid verifies two
// providers with no row are named in registry order, one line each, and that
// a family the probe warnings already report with gapReason's sentence is
// left out. The match is on that sentence, so it is built here from the same
// constant gapReason returns: with two hand-typed copies, a rewording of one
// would print the same gap twice.
func TestMissingProviderWarningsOrderAndTheLineAlreadySaid(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{
		{ID: "zeta/a", ProviderID: "zeta", ModelName: "a"},
		{ID: "corp/b", ProviderID: "corp", ModelName: "b"},
		{ID: "zeta/c", ProviderID: "zeta", ModelName: "c"},
		{ID: "omlx/d", ProviderID: "omlx", ModelName: "d"},
	}}
	line := func(id, models string) string {
		return "provider \"" + id + "\" has no [[providers]] row in " + config.RegistryPath() + ", so wt cannot route " + models +
			"; add the row (`wt model init` adds the default ones) or fix the provider_id"
	}
	got := missingProviderWarnings(cfg, nil)
	want := []string{line("zeta", "zeta/a, zeta/c"), line("corp", "corp/b"), line("omlx", "omlx/d")}
	if !slices.Equal(got, want) {
		t.Errorf("warnings = %q\nwant %q", got, want)
	}
	if reason := gapReason(cfg, "omlx"); reason != noProviderEntryReason {
		t.Fatalf("gapReason = %q, want the shared sentence %q", reason, noProviderEntryReason)
	}
	said := []string{"provider \"omlx\" could not be probed (" + gapReason(cfg, "omlx") + "); its routes are left unchanged"}
	got = missingProviderWarnings(cfg, said)
	if want := want[:2]; !slices.Equal(got, want) {
		t.Errorf("with omlx's gap already said: warnings = %q\nwant %q", got, want)
	}
}

// TestSyncDownWarningIgnoresADuplicatedID verifies that the "refused the
// probe connection ..., so its local routes are treated as stale" warning is
// not raised on the word of an id that more than one registry row carries.
// The warning is decided by id — a routed id that a local row of the stopped
// family holds — and with a second row under the id the route may be another
// row's: a cloud row's, which sync goes on routing, or nobody's, when sync
// leaves the id alone as ambiguous. Sync then removed nothing and still said
// the family's routes were stale, straight after "its route is left as it
// is". A family with a route of its own beside the duplicated id keeps the
// warning.
func TestSyncDownWarningIgnoresADuplicatedID(t *testing.T) {
	cloud := func(name string) config.Model {
		return config.Model{ID: "local/qwen", ProviderID: "openrouter", ModelName: name, Location: config.LocationCloud}
	}
	local := config.Model{ID: "local/qwen", ProviderID: "ollama", ModelName: "qwen:1b", Location: config.LocationLocal}
	other := config.Model{ID: "ollama/other", ProviderID: "ollama", ModelName: "other:1b", Location: config.LocationLocal}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
	}
	for _, tc := range []struct {
		name   string
		models []config.Model
		routed map[string]bool
		want   []string
	}{
		{"the id's only row", []config.Model{local}, map[string]bool{"local/qwen": true}, []string{downProbeWarn}},
		{"a cloud row ahead of the local row", []config.Model{cloud("a/q"), local}, map[string]bool{"local/qwen": true}, nil},
		// The local row first: litellm.RowFamily answers by the first row, so
		// the marked route reads as an ollama row too.
		{"a cloud row behind the local row", []config.Model{local, cloud("a/q")}, map[string]bool{"local/qwen": true}, nil},
		{"the local row between two cloud rows", []config.Model{cloud("a/q"), local, cloud("b/q")}, map[string]bool{"local/qwen": true}, nil},
		{"an unmarked route", []config.Model{local, cloud("a/q")}, map[string]bool{"local/qwen": false}, nil},
		{"a duplicated id beside a route of the family's own", []config.Model{local, cloud("a/q"), other}, map[string]bool{"local/qwen": true, "ollama/other": true}, []string{downProbeWarn}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Providers: []config.Provider{
					{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://127.0.0.1:9"}},
					{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}},
				},
				Models: tc.models,
			}
			untouched, warns := syncUntouchedAndWarnings(cfg, snap, tc.routed)
			if len(untouched) != 0 {
				t.Errorf("untouched = %v, want none: a refused family freezes nothing", untouched)
			}
			if !slices.Equal(warns, tc.want) {
				t.Errorf("warnings:\n got %q\nwant %q", warns, tc.want)
			}
		})
	}
}
