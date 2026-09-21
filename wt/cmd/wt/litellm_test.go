package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
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

// TestLitellmMutatorsRefuseOnConfigError pins that expose/unexpose/sync refuse
// when the registry failed to load (a.cfgErr), like every sibling command.
// Without the guard they act on an empty registry: expose reports "not found"
// and unexpose/sync still write config.yaml and restart the proxy.
func TestLitellmMutatorsRefuseOnConfigError(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	p := litellmEnv(t, body)
	for _, args := range [][]string{{"expose", "ollama/gemma:9b"}, {"unexpose", "ollama/gemma:9b"}, {"sync"}} {
		c := litellmCmd(&app{cfg: &config.Config{}, cfgErr: errors.New("bad toml")})
		var out, errOut bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&errOut)
		c.SetArgs(args)
		err := c.Execute()
		if err == nil || !strings.Contains(err.Error(), "config error: bad toml") {
			t.Fatalf("%v: err = %v, want config error refusal", args, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("%v modified config.yaml:\n%s", args, b)
		}
	}
}

// TestLitellmRejectsBlankIDs pins that a blank or whitespace-only id is
// refused before anything is touched: an empty unexpose id would otherwise
// match (and delete) every row that lacks a model_name.
func TestLitellmRejectsBlankIDs(t *testing.T) {
	body := "model_list:\n  - litellm_params: {model: x}\n  - model_name: a/b\n    litellm_params: {model: y}\n"
	p := litellmEnv(t, body)
	for _, expose := range []bool{true, false} {
		var out, errOut bytes.Buffer
		err := runLitellmChange(&out, &errOut, litellmTestConfig(), expose, []string{"a/b", "  "}, litellmFlags{SkipReadyGate: true})
		if err == nil || !strings.Contains(err.Error(), "blank") {
			t.Fatalf("expose=%v err = %v, want blank-id rejection", expose, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("expose=%v modified config.yaml:\n%s", expose, b)
		}
	}
}

// TestLitellmSyncLeavesUnreachableFamilyAlone pins that when a provider
// family's probe did not succeed (Running is untrustworthy), sync neither
// removes its models' routes nor restarts the proxy, and prints a warning
// naming the family. Otherwise a down ollama daemon wipes every ollama route.
func TestLitellmSyncLeavesUnreachableFamilyAlone(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, additional_drop_params: [reasoning_effort]}\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	p := litellmEnv(t, body)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: false},
		},
	})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("route removed for unreachable family:\n%s", b)
	}
	var doc struct {
		Changed  bool
		Warnings []string
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Changed || len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "ollama") {
		t.Fatalf("stdout = %q (%v), want JSON with changed=false and an ollama warning", out.String(), err)
	}
	out.Reset()
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), "ollama") {
		t.Fatalf("no text-mode warning naming the family: %q", errOut.String())
	}
}

// TestLitellmSyncRemovesRoutesOfRefusedProvider pins the repair case sync
// exists for: a provider stopped outside wt (server refuses connections, so
// its probe is not StatusOK but positively nothing is running) must lose its
// stale routes, with no "probe did not succeed" warning. Unlike an ambiguous
// probe failure, a refused connection is proof the server is down.
func TestLitellmSyncRemovesRoutesOfRefusedProvider(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, additional_drop_params: [reasoning_effort]}\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	p := litellmEnv(t, body)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: false},
		},
	})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "ollama/gemma:9b") {
		t.Fatalf("stale route of a refused provider survived sync:\n%s", b)
	}
	if strings.Contains(errOut.String(), "probe did not succeed") {
		t.Fatalf("unexpected probe warning for a provider known to be down: %q", errOut.String())
	}
}

// TestLitellmSyncNoWarningForUnprobedProvider pins that a registered model on
// a provider with no probe family (retired llamacpp) is left alone silently.
// Before, it produced `provider "" probe did not succeed` in every sync —
// text and the --json warnings modelman parses.
func TestLitellmSyncNoWarningForUnprobedProvider(t *testing.T) {
	body := "model_list:\n  - model_name: llamacpp/m\n    litellm_params: {model: openai/local-model}\n"
	p := litellmEnv(t, body)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "llamacpp", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8080"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "llamacpp/m", ProviderID: "llamacpp", ModelName: "m", Location: config.LocationLocal})
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, true); err != nil {
		t.Fatal(err)
	}
	var doc struct{ Warnings []string }
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || len(doc.Warnings) != 0 {
		t.Fatalf("stdout = %q (%v), want no warnings", out.String(), err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "llamacpp/m") {
		t.Fatalf("unprobed model's route was removed:\n%s", b)
	}
}
