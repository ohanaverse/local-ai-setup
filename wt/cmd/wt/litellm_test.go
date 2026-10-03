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
// modelman's bridge (routed_ids) and its provider policy lookup.
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

// TestLitellmSyncRefusesOnConfigError pins that sync refuses when the
// registry failed to LOAD (a.loadErr), like every sibling command.
// (Validation-only errors are covered by TestLitellmSyncWorksOnValidationOnlyError.)
// Without the guard it acts on an empty registry: sync would still write
// config.yaml and restart the proxy.
func TestLitellmSyncRefusesOnConfigError(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	p := litellmEnv(t, body)
	for _, args := range [][]string{{"sync"}} {
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

// TestLitellmRemovedExposeCommandsFailAndPointToSync pins the hidden stubs
// for the subcommands #179 removed: `wt litellm expose|unexpose` must exit
// non-zero and name `wt litellm sync`, with or without a legacy flag, and
// must not be advertised in help. Without the stubs cobra prints the parent's
// help and exits 0, so a script chaining `wt litellm expose X && ...` would
// report success while writing nothing.
func TestLitellmRemovedExposeCommandsFailAndPointToSync(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	for _, args := range [][]string{
		{"expose", "openrouter/x"},
		{"expose", "openrouter/x", "--json", "--skip-ready-gate", "--dry-run"},
		{"unexpose", "openrouter/x"},
		{"unexpose", "openrouter/x", "--json"},
	} {
		c := litellmCmd(&app{cfg: litellmTestConfig()})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		err := c.Execute()
		if err == nil || !strings.Contains(err.Error(), "wt litellm sync") || !strings.Contains(err.Error(), "removed") {
			t.Errorf("%v: err = %v, want a removal error pointing to `wt litellm sync`", args, err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: []\n" {
		t.Fatalf("removed commands modified config.yaml:\n%s", b)
	}
	var help bytes.Buffer
	c := litellmCmd(&app{cfg: litellmTestConfig()})
	c.SetOut(&help)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"--help"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "sync") || strings.Contains(help.String(), "expose") {
		t.Fatalf("help must list sync and hide expose/unexpose:\n%s", help.String())
	}
}

// TestLitellmStateCommandsRefuseOnConfigError pins that status/on/off/set
// refuse when the config failed to LOAD (a.loadErr; a default cfg would
// overwrite the user's config.toml on Save) and write nothing, and that
// `set` with no flags is a usage error that changes nothing. Guard existed in
// the brief's registration; expected to pass immediately.
func TestLitellmStateCommandsRefuseOnConfigError(t *testing.T) {
	cfg, path := litellmStateEnv(t)
	for _, args := range [][]string{{"status"}, {"on"}, {"off"}, {"set", "--url", "http://x"}, {"sync"}} {
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
// change must persist. (sync is covered by
// TestLitellmSyncWorksOnValidationOnlyError.)
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

// TestLitellmSyncWorksOnValidationOnlyError pins that sync gates only on a
// config LOAD failure. A registry gap in an unrelated model (unknown
// provider: cfgErr set, loadErr nil) must not block modelman's sync
// (chicken-and-egg: modelman is what repairs such gaps): the running healthy
// model is still routed, and the broken model gets no route.
func TestLitellmSyncWorksOnValidationOnlyError(t *testing.T) {
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
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: good, ModelName: "contract-fixture:local", Registered: true, Running: true},
		}})
	c := litellmCmd(a)
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"sync"})
	if err := c.Execute(); err != nil {
		t.Fatalf("sync refused: %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), good) || strings.Contains(string(b), "ghost/m") {
		t.Fatalf("sync did not route the running model, or wrote a ghost route:\n%s", b)
	}
}

// TestLitellmSyncGatesOnLoadErrOnly pins that the guard reads
// a.loadErr and not a.cfgErr by setting only loadErr. In production loadErr
// implies cfgErr (newApp copies it), so this combination cannot occur there;
// it exists to pin which field the guard consults.
func TestLitellmSyncGatesOnLoadErrOnly(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	p := litellmEnv(t, body)
	for _, args := range [][]string{{"sync"}} {
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
		Add:    []string{"openrouter/x/y", "ollama/llama3.2:3b"},
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
// routed (a cloud model, and since #179 Phase B a discovered local model under
// its discovered id, written marked) and a per-id error — so the action names
// are pinned where Sync assigns them, not restated by hand. The injected restart hook fails with
// restart.go's warning text, so the fixture also carries a non-empty
// warnings array for modelman to parse. modelman parses this shape with
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
	const restartWarn = "failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"
	local := []config.Model{litellm.DiscoveredModel("ollama", "llama3.2:3b")}
	res, err := litellm.Sync(cfg, local, litellm.Options{Path: p, Restart: func() []string { return []string{restartWarn} }})
	if err != nil {
		t.Fatal(err)
	}
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Rows(), litellm.RowInfo{ID: "ollama/llama3.2:3b", Managed: true}) {
		t.Fatalf("rows = %v, want the discovered route written marked", f.Rows())
	}
	var out, errOut bytes.Buffer
	if err := reportLitellm(&out, &errOut, res, true); !errors.Is(err, errLitellmIDFailed) {
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
// modelman no longer routes it explicitly, so sync is the only route write.
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

// TestSyncRoutesMlxLMServerAmbiguousLeavesRoutes pins that with two or more
// registered mlx_lm_server pairings whose "+draft-" names match none of the
// served ids (mlx_lm.server lists HF repo ids, never the pairing name), sync
// leaves every pairing's route alone with a warning saying why. wt cannot tell
// which pairing is serving; treating the family as OK removed the serving
// pairing's working route on every sync, silently.
func TestSyncRoutesMlxLMServerAmbiguousLeavesRoutes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"mlx-community/Qwen3.8-27B-4bit"},{"id":"mlx-community/Qwen3.8-0.6B-4bit"}]}`))
	}))
	defer up.Close()
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: up.URL + "/v1"}}},
		Models: []config.Model{
			{ID: "mlx_lm_server/a", ProviderID: "mlx_lm_server", ModelName: "mlx-community/Qwen3.8-27B-4bit+draft-mlx-community/Qwen3.8-0.6B-4bit", Location: config.LocationLocal},
			{ID: "mlx_lm_server/b", ProviderID: "mlx_lm_server", ModelName: "mlx-community/Qwen3.8-27B-4bit+draft-mlx-community/Qwen3.8-1.7B-4bit", Location: config.LocationLocal},
		},
	}
	snap := localmodels.Inventory(cfg)
	if got := desiredLocalIDs(snap); len(got) != 0 {
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
}

// TestSyncRoutesMlxLMServerEmptyAnswerLeavesRoutes pins the same guarantee for
// an EMPTY /v1/models body: with two registered pairings, "answered and listed
// nothing" is no more attributable to a pairing than a foreign id is, so sync
// must leave both routes alone and say so. Reporting OK there removed every
// registered pairing's route silently — a real server lists the pairing it
// loaded, so the only ways to get an empty list are a foreign listener on the
// port or a server still starting up.
func TestSyncRoutesMlxLMServerEmptyAnswerLeavesRoutes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer up.Close()
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: up.URL + "/v1"}}},
		Models: []config.Model{
			{ID: "mlx_lm_server/a", ProviderID: "mlx_lm_server", ModelName: "pair-a", Location: config.LocationLocal},
			{ID: "mlx_lm_server/b", ProviderID: "mlx_lm_server", ModelName: "pair-b", Location: config.LocationLocal},
		},
	}
	snap := localmodels.Inventory(cfg)
	if got := desiredLocalIDs(snap); len(got) != 0 {
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
}

// TestLitellmSyncRoutesDiscoveredModels drives the real `wt litellm sync`
// over a stubbed probe (#179 Phase B): a pulled ollama model with no registry
// overlay gets a marked route under its discovered id, a running mtplx model
// with a two-slash discovered id gets one too, and a family whose probe did
// not succeed (omlx, unreachable) keeps its existing discovered route instead
// of losing it to a probe that could not see it.
func TestLitellmSyncRoutesDiscoveredModels(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
		config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
	)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/llama3.2:3b", ModelName: "llama3.2:3b", Artifact: "llama3.2:3b", ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/org/Qwen3.8-27B", ModelName: "org/Qwen3.8-27B", Artifact: "org/Qwen3.8-27B", ArtifactKnown: true, Running: true},
		},
	})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []litellm.RowInfo{{ID: "omlx/stray-model", Managed: true}, {ID: "ollama/llama3.2:3b", Managed: true}, {ID: "mtplx/org/Qwen3.8-27B", Managed: true}}
	if got := f.Rows(); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestUntrustedFamilies pins which families sync freezes: every probe family
// whose probe is neither OK nor refused (Down) — partial, unreachable, or not
// probed at all — so their discovered routes, which carry no registry id the
// per-id Untouched list could name, are left exactly as they are.
func TestUntrustedFamilies(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable,
		},
		Down: map[string]bool{"mtplx": true},
	}
	if got := untrustedFamilies(snap); !slices.Equal(got, []string{"mlx_lm_server", "omlx"}) {
		t.Fatalf("untrustedFamilies = %v, want [mlx_lm_server omlx]", got)
	}
}

// TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily pins that a family
// sync freezes only for its discovered routes — no registry local model, so
// the per-id Untouched list never names it — still gets the "probe did not
// succeed" warning when config.yaml holds a marked row of that family, in
// the dry run and the real sync alike. Without it a dead omlx probe left
// discovered omlx routes frozen with no word to the user; a family with no
// marked row stays silent, as the everyday unused-provider case must.
func TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily(t *testing.T) {
	const want = `provider "omlx" probe did not succeed (status "partial"); its model routes were left unchanged`
	litellmEnv(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable},
	})
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		var doc struct{ Warnings []string }
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("dryRun=%v: not JSON: %q", dryRun, out.String())
		}
		if !slices.Equal(doc.Warnings, []string{want}) {
			t.Errorf("dryRun=%v: warnings = %q, want only %q (mtplx has no marked row, so it stays silent)", dryRun, doc.Warnings, want)
		}
	}
}

// TestLitellmSyncDryRunMatchesRealSyncForDiscovered pins that `sync --dry-run`
// and the real sync agree once discovered models are in play: over one probe
// the dry run plans exactly the routes the real run then writes — the pulled
// discovered ollama model added, the stale discovered mtplx route removed —
// and both leave alone the untrusted omlx family's route and the hand-written
// row named like a discovered id. They share desiredLocalModels,
// untrustedFamilies and syncUntouchedAndWarnings; a dry run fed anything else
// would promise a plan the real sync does not perform.
func TestLitellmSyncDryRunMatchesRealSyncForDiscovered(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
  - model_name: mtplx/org/gone
    litellm_params: {model: openai/org/gone, api_base: http://localhost:8003/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: ollama/q8
    litellm_params: {model: ollama_chat/q8, timeout: 600}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
		config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
	)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/llama3.2:3b", ModelName: "llama3.2:3b", Artifact: "llama3.2:3b", ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/q8", ModelName: "q8", Artifact: "q8", ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/new-model", ModelName: "new-model", Artifact: "new-model", ArtifactKnown: true, Running: true},
		},
	})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
		t.Fatalf("dry run: %v (stderr %q)", err, errOut.String())
	}
	var dry syncPlanJSON
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
		t.Fatalf("dry run: not JSON: %q", out.String())
	}
	wantAdd, wantRemove := []string{"ollama/llama3.2:3b"}, []string{"mtplx/org/gone"}
	if !slices.Equal(dry.Plan.Add, wantAdd) || !slices.Equal(dry.Plan.Remove, wantRemove) || len(dry.Plan.Adopt)+len(dry.Plan.Rewrite) != 0 {
		t.Fatalf("dry-run plan = %+v, want add %v remove %v", dry.Plan, wantAdd, wantRemove)
	}
	out.Reset()
	if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	var real litellmResultJSON
	if err := json.Unmarshal(out.Bytes(), &real); err != nil {
		t.Fatalf("sync: not JSON: %q", out.String())
	}
	var added, removed []string
	for _, oc := range real.Outcomes {
		switch oc.Action {
		case "routed":
			added = append(added, oc.ID)
		case "unrouted":
			removed = append(removed, oc.ID)
		default:
			t.Errorf("unexpected outcome %+v", oc)
		}
	}
	if !slices.Equal(added, dry.Plan.Add) || !slices.Equal(removed, dry.Plan.Remove) {
		t.Errorf("real sync routed %v unrouted %v, dry run planned add %v remove %v", added, removed, dry.Plan.Add, dry.Plan.Remove)
	}
	if !slices.Equal(real.Warnings, dry.Warnings) || len(dry.Warnings) != 1 {
		t.Errorf("warnings: real %q, dry run %q; want the same single omlx probe warning", real.Warnings, dry.Warnings)
	}
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []litellm.RowInfo{{ID: "omlx/stray-model", Managed: true}, {ID: "ollama/q8"}, {ID: "ollama/llama3.2:3b", Managed: true}}
	if got := f.Rows(); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
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
