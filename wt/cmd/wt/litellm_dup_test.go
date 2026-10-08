package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// dupSyncRegistry is a registry wt's validation refuses ("duplicate model
// id") and `wt litellm sync` still runs on: the id "local/qwen" is on two
// rows, the first under ollama and the second under omlx.
const dupSyncRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:11434"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:8000"

[[models]]
id = "local/qwen"
family = "qwen"
provider_id = "ollama"
model_name = "qwen:1b"
tags = []

[[models]]
id = "local/qwen"
family = "qwen"
provider_id = "omlx"
model_name = "Qwen-4bit"
tags = []
`

// The two routes the id can get, as config.yaml spells the row's `model:`.
const (
	dupOllamaRoute = "model: 'ollama_chat/qwen:1b'"
	dupOmlxRoute   = "model: openai/Qwen-4bit"
)

// dupSyncApp loads dupSyncRegistry from a throwaway home through newApp, as
// the command does, and returns the app and the registry's path.
func dupSyncApp(t *testing.T) (*app, string) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(dupSyncRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.loadErr != nil || a.cfgErr == nil || !strings.Contains(a.cfgErr.Error(), `duplicate model id "local/qwen"`) {
		t.Fatalf("fixture: loadErr = %v, cfgErr = %v, want a validation-only duplicate-id error", a.loadErr, a.cfgErr)
	}
	return a, registry
}

// dupSnapshot is the inventory for dupSyncRegistry with each provider's row
// running or not. The entries are in the order localmodels.Inventory returns
// them: sorted by provider id, one per registry row.
func dupSnapshot(ollama, omlx bool) localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "local/qwen", ModelName: "qwen:1b", Registered: true, ArtifactKnown: true, Running: ollama},
			{ProviderID: "omlx", ModelID: "local/qwen", ModelName: "Qwen-4bit", Artifact: "Qwen-4bit", Registered: true, ArtifactKnown: true, Running: omlx},
		},
	}
}

func dupSyncWarning(registry string) string {
	return `model "local/qwen" is in the registry twice (providers ollama, omlx); its route is left as it is — fix the entry in ` + registry
}

// TestDesiredLocalModelsPairADuplicatedIDByProvider verifies which registry
// row sync routes for an id two rows carry, from what is running: the row of
// the provider that is serving it — the first row's or the second's — and,
// when both providers serve it, neither, with one warning. desiredLocalModels
// used to look the id up and take the first row, so a model running under
// the second provider was handed to sync as the first provider's row, and the
// route for the id could name a server that was not serving it.
func TestDesiredLocalModelsPairADuplicatedIDByProvider(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ollama, omlx  bool
		wantProviders []string
		wantRoute     string
		wantWarnings  int
	}{
		{"running under the second provider", false, true, []string{"omlx"}, dupOmlxRoute, 0},
		{"running under the first provider", true, false, []string{"ollama"}, dupOllamaRoute, 0},
		{"running under both", true, true, []string{"ollama", "omlx"}, "", 1},
		{"running under neither", false, false, nil, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := litellmEnv(t, "model_list: []\n")
			a, registry := dupSyncApp(t)
			snap := dupSnapshot(tc.ollama, tc.omlx)
			stubProbeInventory(t, snap)
			var got []string
			for _, m := range desiredLocalModels(a.cfg, snap) {
				if m.ID != "local/qwen" {
					t.Errorf("desired model %q, want only local/qwen", m.ID)
				}
				// Each entry's own row: the provider and the name go together.
				if want := map[string]string{"ollama": "qwen:1b", "omlx": "Qwen-4bit"}[m.ProviderID]; m.ModelName != want {
					t.Errorf("the %s entry was paired with the row for %q", m.ProviderID, m.ModelName)
				}
				got = append(got, m.ProviderID)
			}
			if !slices.Equal(got, tc.wantProviders) {
				t.Errorf("desired rows' providers = %v, want %v", got, tc.wantProviders)
			}
			var out, errOut bytes.Buffer
			if err := runLitellmSync(&out, &errOut, a.cfg, false, false); err != nil {
				t.Fatal(err)
			}
			body := mustRead(t, yaml)
			if tc.wantRoute == "" {
				if strings.Contains(body, "local/qwen") {
					t.Errorf("a route was written for local/qwen:\n%s", body)
				}
			} else if !strings.Contains(body, tc.wantRoute) || strings.Count(body, "model_name: ") != 1 {
				t.Errorf("config.yaml wants one route with %q:\n%s", tc.wantRoute, body)
			}
			if n := strings.Count(errOut.String(), "warning: "); n != tc.wantWarnings {
				t.Errorf("%d warning(s), want %d:\n%s", n, tc.wantWarnings, errOut.String())
			}
			if tc.wantWarnings == 1 && errOut.String() != "warning: "+dupSyncWarning(registry)+"\n" {
				t.Errorf("stderr = %q", errOut.String())
			}
		})
	}
}

// TestLitellmSyncLeavesTheRouteOfADuplicatedIDAlone runs `wt litellm sync`,
// `--dry-run` and `--json` on a registry with an id on two rows while both
// providers serve it: the route the id already has stays byte for byte, no
// line reports a change to it, and one warning — on stderr, and in the JSON
// document's existing `warnings` array, whose shape does not change — names
// the id, both providers and the registry file. Before this the route was
// rewritten to the last row's server on every sync, without a word.
func TestLitellmSyncLeavesTheRouteOfADuplicatedIDAlone(t *testing.T) {
	yaml := litellmEnv(t, "model_list: []\n")
	a, registry := dupSyncApp(t)
	run := func(args ...string) (stdout, stderr string) {
		t.Helper()
		c := litellmCmd(a)
		var out, errOut bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&errOut)
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("wt litellm %v: %v", args, err)
		}
		return out.String(), errOut.String()
	}
	// The route as a sync wrote it while only ollama served the id.
	stubProbeInventory(t, dupSnapshot(true, false))
	if out, errOut := run("sync"); out != "local/qwen: routed\n" || errOut != "" {
		t.Fatalf("first sync: stdout = %q, stderr = %q", out, errOut)
	}
	before := mustRead(t, yaml)
	if !strings.Contains(before, dupOllamaRoute) {
		t.Fatalf("fixture: the route is not the ollama row's:\n%s", before)
	}

	stubProbeInventory(t, dupSnapshot(true, true))
	warning := dupSyncWarning(registry)
	for _, args := range [][]string{{"sync", "--dry-run"}, {"sync"}} {
		out, errOut := run(args...)
		if out != "" {
			t.Errorf("%v: stdout = %q, want no line: nothing changes", args, out)
		}
		if errOut != "warning: "+warning+"\n" {
			t.Errorf("%v: stderr = %q, want the one warning", args, errOut)
		}
		if got := mustRead(t, yaml); got != before {
			t.Fatalf("%v changed config.yaml:\n%s", args, got)
		}
	}

	out, errOut := run("sync", "--json")
	var doc struct {
		Outcomes []litellmOutcomeJSON `json:"outcomes"`
		Changed  bool                 `json:"changed"`
		Warnings []string             `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("sync --json is not JSON: %v\n%s", err, out)
	}
	if len(doc.Outcomes) != 0 || doc.Changed || !slices.Equal(doc.Warnings, []string{warning}) {
		t.Errorf("sync --json = %s, want no outcome, changed false and the one warning", out)
	}
	if got := jsonKeySet(t, []byte(out)); !slices.Equal(got, []string{"changed", "outcomes", "warnings"}) {
		t.Errorf("sync --json keys = %v: the shape must not change", got)
	}
	if errOut != "" {
		t.Errorf("sync --json stderr = %q, want nothing: the warning is in the document", errOut)
	}
	out, _ = run("sync", "--dry-run", "--json")
	var plan syncPlanJSON
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("sync --dry-run --json is not JSON: %v\n%s", err, out)
	}
	if len(plan.Plan.Add)+len(plan.Plan.Remove)+len(plan.Plan.Rewrite)+len(plan.Plan.Adopt) != 0 || !slices.Equal(plan.Warnings, []string{warning}) {
		t.Errorf("sync --dry-run --json = %s, want an empty plan and the one warning", out)
	}
	if got := mustRead(t, yaml); got != before {
		t.Fatalf("the --json runs changed config.yaml:\n%s", got)
	}

	// With only omlx serving it the id names one server again, and the route
	// follows it — from omlx's row.
	stubProbeInventory(t, dupSnapshot(false, true))
	if out, errOut := run("sync"); out != "local/qwen: rewritten\n" || errOut != "" {
		t.Errorf("sync with one provider serving: stdout = %q, stderr = %q", out, errOut)
	}
	if got := mustRead(t, yaml); !strings.Contains(got, dupOmlxRoute) || strings.Contains(got, dupOllamaRoute) {
		t.Errorf("the route did not follow the provider that serves the id:\n%s", got)
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
	const stale = `provider "ollama" refused the probe connection: nothing is listening there, so its local routes are treated as stale`
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
		{"the id's only row", []config.Model{local}, map[string]bool{"local/qwen": true}, []string{stale}},
		{"a cloud row ahead of the local row", []config.Model{cloud("a/q"), local}, map[string]bool{"local/qwen": true}, nil},
		// The local row first: litellm.RowFamily answers by the first row, so
		// the marked route reads as an ollama row too.
		{"a cloud row behind the local row", []config.Model{local, cloud("a/q")}, map[string]bool{"local/qwen": true}, nil},
		{"the local row between two cloud rows", []config.Model{cloud("a/q"), local, cloud("b/q")}, map[string]bool{"local/qwen": true}, nil},
		{"an unmarked route", []config.Model{local, cloud("a/q")}, map[string]bool{"local/qwen": false}, nil},
		{"a duplicated id beside a route of the family's own", []config.Model{local, cloud("a/q"), other}, map[string]bool{"local/qwen": true, "ollama/other": true}, []string{stale}},
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
