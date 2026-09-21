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
}
