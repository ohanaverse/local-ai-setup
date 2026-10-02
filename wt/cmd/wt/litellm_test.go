package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
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

// TestLitellmDryRunJSONIsDistinguishableFromRealApply pins that --dry-run
// --json output carries a "dry_run" marker: without it, a script previewing
// a change with --dry-run cannot tell the output apart from a real apply
// that happened to change nothing (both show changed:false, action:exposed).
func TestLitellmDryRunJSONIsDistinguishableFromRealApply(t *testing.T) {
	// Sandbox WT_LITELLM_CONFIG the same way every other test in this file
	// does: without it, the real-apply sub-case below would target
	// DefaultPath() (~/.config/litellm/config.yaml) and could write to a
	// developer's live proxy config.
	litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{JSON: true, DryRun: true, SkipReadyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"dry_run":true`) {
		t.Fatalf("dry-run JSON = %s, want a dry_run:true marker", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{JSON: true, SkipReadyGate: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"dry_run"`) {
		t.Fatalf("real-apply JSON = %s, want no dry_run key at all", out.String())
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
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
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
	if strings.TrimSpace(out.String()) != `{"routed":["a/b"],"rows":[{"id":"a/b","managed":false}]}` {
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
// when the registry failed to LOAD (a.loadErr), like every sibling command.
// (Validation-only errors are covered by TestLitellmChangeCommandsWorkOnValidationOnlyError.)
// Without the guard they act on an empty registry: expose reports "not found"
// and unexpose/sync still write config.yaml and restart the proxy.
func TestLitellmMutatorsRefuseOnConfigError(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	p := litellmEnv(t, body)
	for _, args := range [][]string{{"expose", "ollama/gemma:9b"}, {"unexpose", "ollama/gemma:9b"}, {"sync"}} {
		c := litellmCmd(&app{cfg: &config.Config{}, cfgErr: errors.New("bad toml"), loadErr: errors.New("bad toml")})
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
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), true, false); err != nil {
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
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), "ollama") {
		t.Fatalf("no text-mode warning naming the family: %q", errOut.String())
	}
}

// TestLitellmSyncRemovesRoutesOfRefusedProvider pins the repair case sync
// exists for: a provider stopped outside wt (server refuses connections, so
// its probe is not StatusOK but positively nothing is running) must lose its
// stale routes, with no ambiguous "probe did not succeed" warning. Instead it
// gets the dedicated refused warning in every sync rendering, because a local
// route of the family was removed (TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant
// pins when the warning appears and its cloud clause).
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
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "ollama/gemma:9b") {
		t.Fatalf("stale route of a refused provider survived sync:\n%s", b)
	}
	if strings.Contains(errOut.String(), "probe did not succeed") {
		t.Fatalf("unexpected probe warning for a provider known to be down: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "refused the probe connection") {
		t.Fatalf("no refused-daemon warning: %q", errOut.String())
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
	if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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

// TestLitellmStatusAndToggle pins the routing-state commands: `on`/`off` flip
// enabled (routing policy only — never touch the proxy), `set` updates
// url/key independently, status never prints the key (only api_key_set), and
// `on` warns when url/key are unset because agents would fail at launch.
func TestLitellmStatusAndToggle(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runLitellmToggle(&out, &errOut, cfg, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "litellm.url or litellm.api_key is not set") {
		t.Fatalf("no incomplete-config warning: %q", errOut.String())
	}
	u, k := "http://localhost:4000", "sk-123456"
	if err := runLitellmSet(&out, cfg, &u, &k); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLitellmStatus(&out, cfg, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sk-123456") {
		t.Fatalf("status leaked the api key: %s", out.String())
	}
	var st struct {
		Enabled   bool   `json:"enabled"`
		URL       string `json:"url"`
		APIKeySet bool   `json:"api_key_set"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || !st.Enabled || st.URL != u || !st.APIKeySet {
		t.Fatalf("status = %s (%v)", out.String(), err)
	}
	if err := runLitellmToggle(&out, &errOut, cfg, false); err != nil || cfg.IsLitellm() {
		t.Fatalf("off failed: err=%v enabled=%v", err, cfg.IsLitellm())
	}
}

// litellmStateEnv returns a loaded config in a fresh temp XDG dir (empty
// registry) plus the path of the config.toml that UpdateLitellm writes.
func litellmStateEnv(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, filepath.Join(home, ".config", "agent-wt", "config.toml")
}

// TestLitellmStatusTextMasksKey pins that text-mode status shows only
// `***last4` for a normal key and NEVER reveals a key of 4 or fewer chars
// (last-4 of a short key would print the whole secret).
// The short-key case is RED against the brief's implementation; the long-key
// case is existing behavior.
func TestLitellmStatusTextMasksKey(t *testing.T) {
	cfg, _ := litellmStateEnv(t)
	u := "http://localhost:4000"
	for _, tc := range []struct{ key, want string }{{"sk-abcdef1234", "***1234"}, {"abcd", "***"}, {"ab", "***"}} {
		k := tc.key
		if err := runLitellmSet(&bytes.Buffer{}, cfg, &u, &k); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runLitellmStatus(&out, cfg, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "api_key: "+tc.want+"\n") {
			t.Errorf("key %q: status = %q, want api_key: %s", tc.key, out.String(), tc.want)
		}
		if len(tc.key) <= 4 && strings.Contains(strings.TrimPrefix(out.String(), "litellm:"), tc.key) && tc.want == "***" {
			// key text (e.g. "ab") could collide with other words; only fail on the api_key line.
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "api_key:") && strings.Contains(line, tc.key) {
					t.Errorf("short key %q leaked: %q", tc.key, line)
				}
			}
		}
	}
}

// TestLitellmSetIndependentAndClear pins that `set` updates url and key
// independently (a flag left unset never clobbers the other field), that an
// explicit empty --api-key clears the key, and that persisting works: the
// state survives a reload from disk and config.toml is 0600 while a key is
// stored. Existing behavior; expected to pass immediately.
func TestLitellmSetIndependentAndClear(t *testing.T) {
	cfg, path := litellmStateEnv(t)
	u, k := "http://localhost:4000", "sk-secret"
	if err := runLitellmSet(&bytes.Buffer{}, cfg, &u, nil); err != nil {
		t.Fatal(err)
	}
	if err := runLitellmSet(&bytes.Buffer{}, cfg, nil, &k); err != nil {
		t.Fatal(err)
	}
	if cfg.LitellmBaseURL() != u || cfg.LitellmAPIKey() != k {
		t.Fatalf("after url-only then key-only: url=%q key=%q", cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
	}
	u2 := "http://other:4000"
	if err := runLitellmSet(&bytes.Buffer{}, cfg, &u2, nil); err != nil || cfg.LitellmAPIKey() != k {
		t.Fatalf("url-only changed the key: err=%v key=%q", err, cfg.LitellmAPIKey())
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config.toml mode = %v, want 0600 with a stored key", fi.Mode().Perm())
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LitellmBaseURL() != u2 || reloaded.LitellmAPIKey() != k {
		t.Errorf("state did not persist: url=%q key=%q", reloaded.LitellmBaseURL(), reloaded.LitellmAPIKey())
	}
	empty := ""
	if err := runLitellmSet(&bytes.Buffer{}, cfg, nil, &empty); err != nil || cfg.LitellmAPIKey() != "" {
		t.Fatalf("--api-key \"\" did not clear: err=%v key=%q", err, cfg.LitellmAPIKey())
	}
	if cfg.LitellmBaseURL() != u2 {
		t.Errorf("clearing the key changed the url: %q", cfg.LitellmBaseURL())
	}
}

// TestLitellmOffWarnsWhenUnconfigured pins the `off` warning: agents whose
// protocols force LiteLLM still fail to launch without url/key, so the user is
// told. Existing behavior; expected to pass immediately.
func TestLitellmOffWarnsWhenUnconfigured(t *testing.T) {
	cfg, _ := litellmStateEnv(t)
	var out, errOut bytes.Buffer
	if err := runLitellmToggle(&out, &errOut, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "will still fail to launch") {
		t.Errorf("no off warning: %q", errOut.String())
	}
}

// TestLitellmStateCommandsRefuseOnConfigError pins that status/on/off/set
// refuse when the config failed to LOAD (a.loadErr; a default cfg would
// overwrite the user's config.toml on Save) and write nothing, and that
// `set` with no flags is a usage error that changes nothing. Guard existed in
// the brief's registration; expected to pass immediately.
func TestLitellmStateCommandsRefuseOnConfigError(t *testing.T) {
	cfg, path := litellmStateEnv(t)
	for _, args := range [][]string{{"status"}, {"on"}, {"off"}, {"set", "--url", "http://x"}, {"expose", "x"}, {"unexpose", "x"}, {"sync"}} {
		c := litellmCmd(&app{cfg: cfg, cfgErr: errors.New("bad toml"), loadErr: errors.New("bad toml")})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "config error: bad toml") {
			t.Fatalf("%v: err = %v, want config error refusal", args, err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("refused commands wrote config.toml")
	}
	// No-flag `set` on a healthy config is an error and leaves state alone.
	c := litellmCmd(&app{cfg: cfg})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"set"})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("set with no flags: err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("no-flag set wrote config.toml")
	}
}

// validationOnlyApp builds an app via newApp() in a temp XDG dir whose registry
// has a model pointing at a missing provider: Load succeeds but Validate fails
// (the common gap `modelman sync` repairs).
func validationOnlyApp(t *testing.T) (*app, string) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	sample, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := string(sample) + "\n[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n"
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	return a, filepath.Join(home, ".config", "agent-wt", "config.toml")
}

// TestLitellmRoutingCommandsWorkOnValidationOnlyError pins that
// status/on/off/set depend only on wt's own [litellm] state, not on registry
// validity: a model referencing an unknown provider (cfgErr set, loadErr nil)
// must not lock the user out of the only routing control surface, and the
// change must persist. (expose/unexpose/sync are covered by
// TestLitellmChangeCommandsWorkOnValidationOnlyError.)
func TestLitellmRoutingCommandsWorkOnValidationOnlyError(t *testing.T) {
	litellmEnv(t, "model_list: []\n")
	a, path := validationOnlyApp(t)
	if a.cfgErr == nil || a.loadErr != nil {
		t.Fatalf("fixture: cfgErr=%v loadErr=%v, want validation-only error", a.cfgErr, a.loadErr)
	}
	run := func(args ...string) error {
		c := litellmCmd(a)
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		return c.Execute()
	}
	for _, args := range [][]string{{"set", "--url", "http://localhost:4000", "--api-key", "sk-abcdef"}, {"on"}, {"status"}, {"off"}, {"on"}} {
		if err := run(args...); err != nil {
			t.Fatalf("%v refused on validation-only error: %v", args, err)
		}
	}
	if !a.cfg.IsLitellm() || a.cfg.LitellmAPIKey() != "sk-abcdef" {
		t.Fatalf("state not applied: %+v", a.cfg.LitellmTable)
	}
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "sk-abcdef") {
		t.Fatalf("state not persisted (err %v):\n%s", err, b)
	}
}

// TestLitellmChangeCommandsWorkOnValidationOnlyError pins that expose/unexpose/
// sync gate only on a config LOAD failure. A registry gap in an unrelated model
// (unknown provider: cfgErr set, loadErr nil) must not block modelman's
// expose/unexpose (chicken-and-egg: modelman is what repairs such gaps), while
// the broken model itself is still reported per id, exit non-zero, without
// blocking the healthy ids in the same batch.
func TestLitellmChangeCommandsWorkOnValidationOnlyError(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	a, _ := validationOnlyApp(t)
	if a.cfgErr == nil || a.loadErr != nil {
		t.Fatalf("fixture: cfgErr=%v loadErr=%v", a.cfgErr, a.loadErr)
	}
	// sync routes the fixture's cloud models too; an unset secret_ref is a
	// per-id build error (exit 1), which is not what this test is about.
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("PINNED_CLOUD_API_KEY", "sk-test")
	const good = "ollama/contract-fixture:local"
	run := func(args ...string) (string, error) {
		c := litellmCmd(a)
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		err := c.Execute()
		return out.String(), err
	}
	if _, err := run("expose", good, "--skip-ready-gate"); err != nil {
		t.Fatalf("expose healthy model refused: %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), good) {
		t.Fatalf("route not written:\n%s", b)
	}
	if _, err := run("unexpose", good); err != nil {
		t.Fatalf("unexpose refused: %v", err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), good) {
		t.Fatalf("route not removed:\n%s", b)
	}
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: good, ModelName: "contract-fixture:local", Registered: true, Running: true},
		}})
	if _, err := run("sync"); err != nil {
		t.Fatalf("sync refused: %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), good) {
		t.Fatalf("sync did not route running model:\n%s", b)
	}
	if _, err := run("unexpose", good); err != nil {
		t.Fatal(err)
	}
	out, err := run("expose", "ghost/m", good, "--skip-ready-gate", "--json")
	if err == nil {
		t.Fatal("want non-nil error: broken model rejected")
	}
	var got struct {
		Outcomes []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Error  string `json:"error"`
		} `json:"outcomes"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil || len(got.Outcomes) != 2 {
		t.Fatalf("output = %s (%v)", out, jerr)
	}
	if got.Outcomes[0].Error == "" || got.Outcomes[1].Error != "" || got.Outcomes[1].Action != "exposed" {
		t.Fatalf("outcomes = %+v", got.Outcomes)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), good) || strings.Contains(string(b), "ghost/m") {
		t.Fatalf("mixed batch: healthy route missing or ghost route written:\n%s", b)
	}
}

// TestLitellmExposeEmptyModelNameUnderValidationError pins that a model with
// an empty model_name (a registry validation gap) is reported per id in the
// JSON outcome and writes no route, leaving config.yaml byte-identical, even
// though expose no longer refuses on validation-only errors.
func TestLitellmExposeEmptyModelNameUnderValidationError(t *testing.T) {
	// Seed the settings wt would add so a no-op leaves the bytes untouched.
	body := "model_list: []\nlitellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	p := litellmEnv(t, body)
	a, _ := validationOnlyApp(t)
	a.cfg.Models = append(a.cfg.Models, config.Model{ID: "ollama/blank", ProviderID: "ollama", Location: config.LocationLocal})
	c := litellmCmd(a)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"expose", "ollama/blank", "--skip-ready-gate", "--json"})
	if err := c.Execute(); err == nil {
		t.Fatal("want non-nil error")
	}
	if !strings.Contains(out.String(), "empty model_name") {
		t.Fatalf("outcome lacks per-id error: %s", out.String())
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("config.yaml modified:\n%s", b)
	}
}

// TestLitellmChangeCommandsGateOnLoadErrOnly pins that the guard reads
// a.loadErr and not a.cfgErr by setting only loadErr. In production loadErr
// implies cfgErr (newApp copies it), so this combination cannot occur there;
// it exists to pin which field the guard consults.
func TestLitellmChangeCommandsGateOnLoadErrOnly(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	p := litellmEnv(t, body)
	for _, args := range [][]string{{"expose", "ollama/gemma:9b"}, {"unexpose", "ollama/gemma:9b"}, {"sync"}} {
		c := litellmCmd(&app{cfg: &config.Config{}, loadErr: errors.New("bad toml")})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "config error: bad toml") {
			t.Fatalf("%v: err = %v, want refusal", args, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("%v modified config.yaml", args)
		}
	}
}

// litellmCloudTestConfig is litellmTestConfig plus one openrouter cloud
// model, the shape sync now routes unconditionally (#179).
func litellmCloudTestConfig() *config.Config {
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: "openrouter", Location: config.LocationCloud,
		Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"},
	})
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
	return cfg
}

// TestLitellmSyncDryRunJSON pins `wt litellm sync --dry-run --json`: the plan
// is printed in the shape modelman parses and config.yaml is not written.
func TestLitellmSyncDryRunJSON(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	before, _ := os.ReadFile(p)
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, true); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DryRun bool `json:"dry_run"`
		Plan   struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if !doc.DryRun || !slices.Equal(doc.Plan.Add, []string{"openrouter/x"}) || len(doc.Plan.Remove) != 0 {
		t.Fatalf("doc = %+v", doc)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatalf("dry run wrote config.yaml:\n%s", after)
	}
}

// jsonKeySet returns a JSON object's top-level keys, sorted, or fails the test
// (helper for the contract pins below).
func jsonKeySet(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestLitellmSyncDryRunJSONMatchesContract pins the `sync --dry-run --json`
// document against the shared cross-language fixture's sync_dry_run block.
// That block has no reader yet — modelman's wt_bridge parses only the change,
// list, providers and status shapes, and nothing calls `wt litellm sync` — so
// this is not guarding a live consumer. It is guarding the shared fixture
// itself: the block is part of the documented cross-language contract, and
// without this pin a field added to syncPlanJSON (or to its nested plan) would
// silently leave the fixture describing a shape wt no longer emits, for
// whoever reads it next. A field added to syncPlanJSON must reach the fixture
// in the same change.
func TestLitellmSyncDryRunJSONMatchesContract(t *testing.T) {
	plan := litellm.SyncPlan{
		Add:    []string{"openrouter/x/y"},
		Adopt:  []string{"ollama/gemma:9b"},
		Remove: []string{"openrouter/old"},
		Errors: []litellm.Outcome{{ID: "ghost/m", Err: errors.New(`unknown provider "ghost"`)}},
	}
	var out, errOut bytes.Buffer
	// No probe warnings in this rendering; a plan with errors exits 1, like a
	// real sync.
	if err := reportSyncPlan(&out, &errOut, plan, nil, true); !errors.Is(err, errLitellmIDFailed) {
		t.Fatalf("reportSyncPlan err = %v, want errLitellmIDFailed", err)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	block, ok := fixture["sync_dry_run"]
	if !ok {
		t.Fatal("fixture has no sync_dry_run block")
	}
	var got, want any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if err := json.Unmarshal(block, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sync --dry-run --json =\n%s\nfixture =\n%s", out.String(), block)
	}
	// Pin the key sets too, so a field that is added to syncPlanJSON but left
	// at its zero value (and would otherwise slip past a value comparison only
	// because the fixture happens not to exercise it) still fails here.
	if gotKeys, wantKeys := jsonKeySet(t, out.Bytes()), jsonKeySet(t, block); !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("root keys = %v, fixture = %v", gotKeys, wantKeys)
	}
	var gotDoc, wantDoc map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &gotDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(block, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if gotKeys, wantKeys := jsonKeySet(t, gotDoc["plan"]), jsonKeySet(t, wantDoc["plan"]); !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("plan keys = %v, fixture = %v", gotKeys, wantKeys)
	}
}

// TestLitellmSyncJSONMatchesContract pins `wt litellm sync --json` against the
// shared fixture's sync block. The result comes from a real litellm.Sync over
// a config.yaml that exercises every outcome — unrouted, rewritten, adopted,
// routed and a per-id error — so the action names are pinned where Sync
// assigns them, not restated by hand. modelman parses this shape with
// parse_change_result (modelman/tests/contracts/test_litellm_cli_fixture.py).
func TestLitellmSyncJSONMatchesContract(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: openrouter/old
    litellm_params: {model: openrouter/old}
    model_info: {wt_managed: true}
  - model_name: openrouter/x
    litellm_params: {model: openrouter/STALE}
    model_info: {wt_managed: true}
  - model_name: openrouter/adopt
    litellm_params: {model: openrouter/adopt}
`)
	cfg := litellmCloudTestConfig()
	for _, id := range []string{"adopt", "new", "bad"} {
		name := id
		if id == "bad" {
			name = ""
		}
		cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/" + id, ProviderID: "openrouter", ModelName: name, Location: config.LocationCloud})
	}
	res, err := litellm.Sync(cfg, nil, litellm.Options{Path: p, Restart: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := reportLitellm(&out, &errOut, res, true, false); !errors.Is(err, errLitellmIDFailed) {
		t.Fatalf("reportLitellm err = %v, want errLitellmIDFailed", err)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	block, ok := fixture["sync"]
	if !ok {
		t.Fatal("fixture has no sync block")
	}
	var got, want any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if err := json.Unmarshal(block, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sync --json =\n%s\nfixture =\n%s", out.String(), block)
	}
}

// downProbeWarn is the warning a provider whose server refused the probe
// connection produces in both sync modes, spelled out once so the dry-run and
// real-run tests compare the same string.
const downProbeWarn = `provider "ollama" refused the probe connection: nothing is listening there, so its local routes are treated as stale`

// TestLitellmSyncDryRunReportsProbeWarnings pins that sync --dry-run prints the
// same probe warnings the real sync prints — before, the dry run returned
// before the warning loop, so it could promise a clean run that then arrived
// with warnings. It also pins the refused-daemon warning's content and the
// plan it accompanies: the dead daemon's local routes go (nothing is
// listening) while unrelated cloud routes (openrouter/x) are still added, and
// sync reports that in both renderings.
func TestLitellmSyncDryRunReportsProbeWarnings(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n"
	p := litellmEnv(t, body)
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
	}
	stubProbeInventory(t, snap)
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, true); err != nil {
		t.Fatal(err)
	}
	var dry struct {
		Warnings []string `json:"warnings"`
		Plan     struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !slices.Equal(dry.Warnings, []string{downProbeWarn}) {
		t.Fatalf("dry-run warnings = %q, want [%q]", dry.Warnings, downProbeWarn)
	}
	if !slices.Equal(dry.Plan.Add, []string{"openrouter/x"}) || !slices.Equal(dry.Plan.Remove, []string{"ollama/gemma:9b"}) {
		t.Fatalf("plan = %+v, want openrouter/x added and the dead daemon's route removed", dry.Plan)
	}
	if after, _ := os.ReadFile(p); string(after) != body {
		t.Fatalf("dry run wrote config.yaml:\n%s", after)
	}
	// Text-mode dry run prints the same warning on stderr.
	out.Reset()
	errOut.Reset()
	if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), false, true); err != nil {
		t.Fatal(err)
	}
	if errOut.String() != "warning: "+downProbeWarn+"\n" {
		t.Fatalf("text dry run = %q, want the refused-provider warning", errOut.String())
	}
	// The real sync (the seam's stub persists for the recheck probe) reports
	// exactly the same warnings, appended after its restart warnings, and
	// performs exactly this plan.
	out.Reset()
	errOut.Reset()
	if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, false); err != nil {
		t.Fatal(err)
	}
	var real struct {
		Warnings []string `json:"warnings"`
		Outcomes []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(out.Bytes(), &real); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !slices.Equal(real.Warnings, []string{downProbeWarn}) {
		t.Fatalf("real-sync warnings = %q, want only the refused-provider warning; the dry run must not promise less", real.Warnings)
	}
	actions := map[string]string{}
	for _, o := range real.Outcomes {
		actions[o.ID] = o.Action
	}
	if actions["ollama/gemma:9b"] != "unrouted" || actions["openrouter/x"] != "routed" {
		t.Fatalf("outcomes = %+v, want gemma unrouted and openrouter/x routed", real.Outcomes)
	}
}

// TestLitellmListTextOutput pins list's human-readable output: one line per
// row, a wt row as its bare id (so `wt litellm list | grep -x <id>` and the
// guides' "list of routed ids" still hold) and a hand-written row as
// "id<TAB>(hand-written)". A user reads the marker to tell which rows are
// their own; a missing one misleads them about which rows wt may rewrite or
// remove.
func TestLitellmListTextOutput(t *testing.T) {
	litellmEnv(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
`)
	var out bytes.Buffer
	if err := runLitellmList(&out, false); err != nil {
		t.Fatal(err)
	}
	const want = "ollama/gemma:9b\nhand/alias\t(hand-written)\n"
	if got := out.String(); got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}

// TestLitellmListJSONRows pins list's ownership rows next to the legacy
// "routed" id list modelman already reads.
func TestLitellmListJSONRows(t *testing.T) {
	litellmEnv(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
`)
	var out bytes.Buffer
	if err := runLitellmList(&out, true); err != nil {
		t.Fatal(err)
	}
	const want = `{"routed":["ollama/gemma:9b","hand/alias"],"rows":[{"id":"ollama/gemma:9b","managed":true},{"id":"hand/alias","managed":false}]}`
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("list = %s\nwant   %s", got, want)
	}
}

// TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant pins that a stopped
// provider (refused probe) warns only when the refusal affects a route: a
// local route of its family in config.yaml (treated as stale), or a registry
// cloud model it serves (routed, but failing until it is back). A stopped
// provider with neither — the everyday state of an unused mtplx — is silent.
func TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
	}
	cases := []struct {
		name string
		body string
		cfg  func() *config.Config
		want []string
	}{
		{"nothing routed, no cloud models", "model_list: []\n", litellmTestConfig, nil},
		{"local route present", "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n", litellmTestConfig,
			[]string{`provider "ollama" refused the probe connection: nothing is listening there, so its local routes are treated as stale`}},
		{"ollama cloud model", "model_list: []\n", func() *config.Config {
			cfg := litellmTestConfig()
			cfg.Models = append(cfg.Models, config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", ModelName: "glm:cloud", Location: config.LocationCloud})
			return cfg
		}, []string{`provider "ollama" refused the probe connection: nothing is listening there; its cloud models stay routed but fail until it is back up`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			litellmEnv(t, c.body)
			stubProbeInventory(t, snap)
			var out, errOut bytes.Buffer
			if err := runLitellmSync(&out, &errOut, c.cfg(), true, true); err != nil {
				t.Fatal(err)
			}
			var dry struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, out.String())
			}
			if !slices.Equal(dry.Warnings, c.want) {
				t.Fatalf("warnings = %q, want %q", dry.Warnings, c.want)
			}
		})
	}
}

// TestDesiredLocalIDsOllamaFollowsPulled pins #179's rule for which local
// registry models sync routes: any running one, plus a registered ollama model
// that is pulled whether or not it is loaded. ollama lazy-loads on request and
// unloads idle models, so keying ollama on "loaded" made a flag-only start get
// no route and made every sync after an idle unload drop a working route.
// "Pulled" must mean the probe actually saw the artifact (ArtifactKnown), the
// rule is registry-only, and it must not widen to single-model families, which
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
	want := []string{"ollama/pulled:1", "mtplx/live", "ollama/loaded:1"}
	if got := desiredLocalIDs(snap); !slices.Equal(got, want) {
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
		if got := desiredLocalIDs(c.snap); !slices.Equal(got, c.want) {
			t.Errorf("%s: desiredLocalIDs = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSyncRoutesMlxLMServerByProbe pins that `wt litellm sync` owns the
// mlx_lm_server route lifecycle (#179): wt has no start backend for it and
// modelman no longer exposes it explicitly, so sync is the only route write.
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
	if got := desiredLocalIDs(snap); !slices.Equal(got, []string{"mlx_lm_server/pair"}) {
		t.Errorf("answering: desiredLocalIDs = %v, want the running pairing", got)
	}
	untouched, warns := syncUntouchedAndWarnings(cfg, snap, routed)
	if len(untouched) != 0 || len(warns) != 0 {
		t.Errorf("answering: untouched = %v, warnings = %v; want neither", untouched, warns)
	}

	cfg = cfgFor(goneURL)
	snap = localmodels.Inventory(cfg)
	if got := desiredLocalIDs(snap); len(got) != 0 {
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
