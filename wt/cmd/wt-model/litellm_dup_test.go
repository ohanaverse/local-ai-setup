package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// dupSyncRegistry is a registry wt's validation refuses ("duplicate model
// id") and `wt-model litellm sync` still runs on: the id "local/qwen" is on two
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

// dupSyncConfig loads dupSyncRegistry from a throwaway home through
// config.Load, as the command does, and returns the config and the
// registry's path. The load succeeds; validation fails on the id.
func dupSyncConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	registry := modelHome(t, dupSyncRegistry)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("fixture: loadErr = %v, want a registry that loads", err)
	}
	if verr := cfg.Validate(); verr == nil || !strings.Contains(verr.Error(), `duplicate model id "local/qwen"`) {
		t.Fatalf("fixture: cfgErr = %v, want a validation-only duplicate-id error", verr)
	}
	return cfg, registry
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
	h := testHost()
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
			cfg, registry := dupSyncConfig(t)
			snap := dupSnapshot(tc.ollama, tc.omlx)
			stubProbeInventory(h, snap)
			var got []string
			for _, m := range desiredModels(cfg, snap) {
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
			if err := h.runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
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

// TestLitellmSyncLeavesTheRouteOfADuplicatedIDAlone runs `wt-model litellm sync`,
// `--dry-run` and `--json` on a registry with an id on two rows while both
// providers serve it: the route the id already has stays byte for byte, no
// line reports a change to it, and one warning — on stderr, and in the JSON
// document's existing `warnings` array, whose shape does not change — names
// the id, both providers and the registry file. Before this the route was
// rewritten to the last row's server on every sync, without a word.
func TestLitellmSyncLeavesTheRouteOfADuplicatedIDAlone(t *testing.T) {
	h := testHost()
	yaml := litellmEnv(t, "model_list: []\n")
	_, registry := dupSyncConfig(t)
	run := func(args ...string) (stdout, stderr string) {
		t.Helper()
		out, errOut, err := execWTModel(t, h, append([]string{"litellm"}, args...)...)
		if err != nil {
			t.Fatalf("wt-model litellm %v: %v", args, err)
		}
		return out, errOut
	}
	// The route as a sync wrote it while only ollama served the id.
	stubProbeInventory(h, dupSnapshot(true, false))
	if out, errOut := run("sync"); out != "local/qwen: routed\n" || errOut != "" {
		t.Fatalf("first sync: stdout = %q, stderr = %q", out, errOut)
	}
	before := mustRead(t, yaml)
	if !strings.Contains(before, dupOllamaRoute) {
		t.Fatalf("fixture: the route is not the ollama row's:\n%s", before)
	}

	stubProbeInventory(h, dupSnapshot(true, true))
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
		Outcomes []litellm.OutcomeJSON `json:"outcomes"`
		Changed  bool                  `json:"changed"`
		Warnings []string              `json:"warnings"`
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
	var plan litellm.SyncPlanJSON
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
	stubProbeInventory(h, dupSnapshot(false, true))
	if out, errOut := run("sync"); out != "local/qwen: rewritten\n" || errOut != "" {
		t.Errorf("sync with one provider serving: stdout = %q, stderr = %q", out, errOut)
	}
	if got := mustRead(t, yaml); !strings.Contains(got, dupOmlxRoute) || strings.Contains(got, dupOllamaRoute) {
		t.Errorf("the route did not follow the provider that serves the id:\n%s", got)
	}
}
