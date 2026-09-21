package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func litellmTestConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "claude", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
		},
	}
}

// litellmEnv points the config path at a temp file and the restart command
// at a no-op so no test can touch the real proxy or config.
func litellmEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	return p
}

// TestLitellmExposeJSONMatchesContract pins the --json shape against the
// shared cross-language fixture: modelman's bridge parses exactly these keys,
// so renaming one here without the fixture failing would break modelman
// silently at runtime.
func TestLitellmExposeJSONMatchesContract(t *testing.T) {
	litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b", "claude/sonnet"},
		litellmFlags{JSON: true, SkipReadyGate: true})
	if err == nil {
		t.Fatal("want a non-nil error: one id was rejected")
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var change map[string]any
	if err := json.Unmarshal(fixture["change"], &change); err != nil {
		t.Fatal(err)
	}
	for k := range change {
		if _, ok := got[k]; !ok {
			t.Errorf("output lacks contract key %q: %v", k, got)
		}
	}
	outcomes := got["outcomes"].([]any)
	first := outcomes[0].(map[string]any)
	second := outcomes[1].(map[string]any)
	if first["action"] != "exposed" || second["error"] == nil || got["changed"] != true {
		t.Fatalf("got = %v", got)
	}
}

// TestLitellmExposeGateAndDryRun pins that the CLI enforces the ready gate by
// default (a not-ready local model is refused), that --skip-ready-gate lifts
// it, and that --dry-run validates without writing anything.
func TestLitellmExposeGateAndDryRun(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{}); err == nil ||
		!strings.Contains(out.String()+errOut.String(), "not ready") {
		t.Fatalf("not-ready model accepted: err=%v out=%s", err, out.String())
	}
	out.Reset()
	before, _ := os.ReadFile(p)
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{DryRun: true, SkipReadyGate: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(before) {
		t.Fatal("--dry-run wrote to config.yaml")
	}
}

// TestLitellmSyncUsesLiveInventory pins that sync derives "running" from the
// live inventory (never modelman's flag): a running registered model gets a
// route and an unrouted-but-stopped one keeps none.
func TestLitellmSyncUsesLiveInventory(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: true},
	}})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "model_name: 'ollama/gemma:9b'") {
		t.Fatalf("running model not routed:\n%s", b)
	}
}

// TestLitellmListAndProviders pins the read-only commands' JSON shapes used by
// modelman's EXPOSED column and its provider policy lookup.
func TestLitellmListAndProviders(t *testing.T) {
	litellmEnv(t, "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n")
	var out bytes.Buffer
	if err := runLitellmList(&out, true); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"routed":["a/b"]}` {
		t.Fatalf("list = %s", out.String())
	}
	out.Reset()
	if err := runLitellmProviders(&out, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers map[string]struct{ Cloud bool } `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.Providers["openrouter"].Cloud || got.Providers["ollama"].Cloud {
		t.Fatalf("providers = %s (%v)", out.String(), err)
	}
}
