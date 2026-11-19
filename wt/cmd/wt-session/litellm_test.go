package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// execLitellm runs `wt litellm <args>` over a, discarding both streams, and
// returns the command's error.
func execLitellm(a *app, args ...string) error {
	c := litellmCmd(a)
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	return c.Execute()
}

// routeRows opens the config.yaml at p and returns its rows.
func routeRows(t *testing.T, p string) []litellm.RowInfo {
	t.Helper()
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return f.Rows()
}

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

// constProbe is a litellm.Probe that answers snap every time.
func constProbe(s localmodels.Snapshot) litellm.Probe {
	return func(*config.Config) localmodels.Snapshot { return s }
}

// recheckIDs is the desired local id set litellm.SyncInputs' under-the-lock
// Recheck answers for snap.
func recheckIDs(cfg *config.Config, snap localmodels.Snapshot) []string {
	_, o, _ := litellm.SyncInputs(cfg, constProbe(snap))
	return o.Recheck()
}

// desiredModels is the local models litellm.SyncInputs hands to sync for snap.
func desiredModels(cfg *config.Config, snap localmodels.Snapshot) []config.Model {
	desired, _, _ := litellm.SyncInputs(cfg, constProbe(snap))
	return desired
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
// live inventory (never a stored flag): a running registered model gets a
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

// TestLitellmSyncWaitsForTheProxy pins #379 at the command: `wt litellm
// sync` (which `wt cloud-sync`, `wt-model model add|edit|rm|init` and the Models tab
// run after a registry write) waited for nothing after its restart, so it
// returned while the proxy still refused connections. With a proxy listening
// at the [litellm] url, the restart — the WT_LITELLM_RESTART_CMD override
// here, which the lifecycle path waits after too — is followed by the wait,
// before the command returns, and a proxy that never answers is a warning on
// stderr, not a failed sync.
func TestLitellmSyncWaitsForTheProxy(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	restarts := filepath.Join(t.TempDir(), "restarts")
	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: true},
		}})
	const url = "http://proxy.test:4000"
	var steps []string
	t.Cleanup(litellm.StubProxyReadinessForTest(
		func(_ context.Context, u string, _ time.Duration) bool {
			steps = append(steps, "probe "+u)
			return true
		},
		func(_ context.Context, u string, d time.Duration) error {
			b, _ := os.ReadFile(restarts)
			steps = append(steps, fmt.Sprintf("wait %s %s after %q", u, d, b))
			return errors.New("LiteLLM proxy not ready at " + u + "/health/liveliness: context deadline exceeded")
		},
	))
	cfg := litellmTestConfig()
	cfg.SetLitellmForTest(config.LitellmState{URL: url})

	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
		t.Fatalf("a proxy that did not answer failed the sync: %v", err)
	}
	want := []string{"probe " + url, fmt.Sprintf("wait %s %s after %q", url, litellm.ProxyReadyTimeout, "restart\n")}
	if !slices.Equal(steps, want) {
		t.Fatalf("steps = %q, want %q", steps, want)
	}
	if !strings.Contains(errOut.String(), "warning: LiteLLM proxy not ready at "+url) {
		t.Fatalf("stderr = %q, want the failed wait as a warning", errOut.String())
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "model_name: 'ollama/gemma:9b'") {
		t.Fatalf("route not written:\n%s", b)
	}
}

// TestLitellmRestartWaitsAndReports pins `wt litellm restart`: the restart
// `wt litellm sync` runs (here the WT_LITELLM_RESTART_CMD override) and the
// wait after it, taken with no pre-restart probe so a proxy that was down is
// waited for too (the probe stub says nothing is listening), one line on
// stdout when the proxy answers again, and an error naming the cause when
// the restart command fails or the proxy never answers. bin/update/litellm
// runs it after an upgrade in place of `launchctl kickstart -k`, which kills
// a request in flight after 5 seconds; a failure that exited 0 would leave
// the updater reporting a proxy it never restarted.
func TestLitellmRestartWaitsAndReports(t *testing.T) {
	const url = "http://proxy.test:4000"
	for _, tc := range []struct {
		name, cmd string
		waitErr   error
		wantErr   string
	}{
		{name: "restarted and answering", cmd: "echo restart >> RESTARTS"},
		{name: "the restart command fails", cmd: "echo restart >> RESTARTS; exit 3", wantErr: "exit status 3"},
		{name: "the proxy never answers", cmd: "echo restart >> RESTARTS", waitErr: errors.New("LiteLLM proxy not ready at " + url + "/health/liveliness: context deadline exceeded"),
			wantErr: "LiteLLM proxy not ready at " + url},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restarts := filepath.Join(t.TempDir(), "restarts")
			t.Setenv("WT_LITELLM_RESTART_CMD", strings.ReplaceAll(tc.cmd, "RESTARTS", restarts))
			var steps []string
			t.Cleanup(litellm.StubProxyReadinessForTest(
				func(_ context.Context, u string, _ time.Duration) bool {
					steps = append(steps, "probe "+u)
					return false
				},
				func(_ context.Context, u string, _ time.Duration) error {
					b, _ := os.ReadFile(restarts)
					steps = append(steps, fmt.Sprintf("wait %s after %q", u, b))
					return tc.waitErr
				},
			))
			cfg := litellmTestConfig()
			cfg.SetLitellmForTest(config.LitellmState{URL: url})

			var out bytes.Buffer
			err := runLitellmRestart(&out, cfg)
			if want := []string{fmt.Sprintf("wait %s after %q", url, "restart\n")}; !slices.Equal(steps, want) {
				t.Errorf("steps = %q, want %q", steps, want)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if got, want := out.String(), "restarted the LiteLLM proxy at "+url+"\n"; got != want {
					t.Errorf("stdout = %q, want %q", got, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if out.Len() != 0 {
				t.Errorf("a failed restart printed %q on stdout", out.String())
			}
		})
	}
}

// TestLitellmRestartHidesAnAtURL pins the success line's one url rule
// (#640): a stored pair url holding an '@' (hand-written, or set by an older
// wt) is printed only as config.ProxyURLHidden, never in part.
func TestLitellmRestartHidesAnAtURL(t *testing.T) {
	const url = "http://USERX:PASSX@proxy.test:4000"
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	t.Cleanup(litellm.StubProxyReadinessForTest(
		func(context.Context, string, time.Duration) bool { return false },
		func(context.Context, string, time.Duration) error { return nil },
	))
	cfg := litellmTestConfig()
	cfg.SetLitellmForTest(config.LitellmState{URL: url})
	var out bytes.Buffer
	if err := runLitellmRestart(&out, cfg); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got, want := out.String(), "restarted the LiteLLM proxy at "+config.ProxyURLHidden+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestLitellmListAndProviders pins the read-only commands' JSON shapes: the
// "routed" id list and ownership rows of `list --json`, and the per-provider
// policy of `providers --json` (cloud or not). They are what a script reads
// to learn what the proxy serves; a renamed key breaks it.
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
		err := execLitellm(&app{hostSet: true, hostCfg: &config.Config{}, hostCfgErr: errors.New("bad toml"), hostLoadErr: errors.New("bad toml")}, args...)
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
	// The row carries an api_base so the write has nothing to repair (#202):
	// this test is about the family's routes being left alone.
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, api_base: \"http://localhost:11434\", additional_drop_params: [reasoning_effort]}\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n"
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
// Before, it produced `provider "" probe did not succeed` in every sync, in
// the text output and in the --json warnings array alike.
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

// TestLitellmStatusAndSet pins the proxy url/key functions the host keeps:
// `set` updates url/key in the wt-model home, and status never prints the key
// (only api_key_set) nor the retired enabled.
func TestLitellmStatusAndSet(t *testing.T) {
	litellmStateEnv(t)
	var out, errOut bytes.Buffer
	u, k := "http://localhost:4000", "sk-123456"
	if err := runLitellmSet(&out, &u, &k); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLitellmStatus(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sk-123456") {
		t.Fatalf("status leaked the api key: %s", out.String())
	}
	var st struct {
		URL       string `json:"url"`
		APIKeySet bool   `json:"api_key_set"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || strings.Contains(out.String(), "enabled") || st.URL != u || !st.APIKeySet {
		t.Fatalf("status = %s (%v)", out.String(), err)
	}
}

// litellmStateEnv is a scratch config home (empty registry) for the
// proxy-pair commands, and returns the legacy config.toml path — the
// read-only [litellm] fallback. The commands under test read and write the
// proxy pair alone (the wt-model home), so nothing here loads wt's own
// config.
func litellmStateEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	return filepath.Join(home, ".config", "agent-wt", "config.toml")
}

// resolveProxy is the proxy pair as every reader resolves it (ResolveProxy),
// so these tests assert persistence the way production sees it.
func resolveProxy(t *testing.T) config.ProxyPair {
	t.Helper()
	pair, err := config.ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// TestLitellmStatusTextMasksKey pins that text-mode status shows only
// `***last4` for a normal key and NEVER reveals a key of 4 or fewer chars
// (last-4 of a short key would print the whole secret), and that a home pair
// prints nothing on stderr (the fallback note is only for a legacy pair).
func TestLitellmStatusTextMasksKey(t *testing.T) {
	litellmStateEnv(t)
	u := "http://localhost:4000"
	for _, tc := range []struct{ key, want string }{{"sk-abcdef1234", "***1234"}, {"abcd", "***"}, {"ab", "***"}} {
		k := tc.key
		if err := runLitellmSet(&bytes.Buffer{}, &u, &k); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := runLitellmStatus(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("key %q: stderr = %q, want none for a home pair", tc.key, errOut.String())
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

// TestLitellmSetIndependentAndClear pins `set`'s Changed semantics (C4) and
// where the pair lives now: a one-flag first set over a legacy pair is refused
// (it would drop the other field), then a flag left out never clobbers the other field,
// --api-key "" clears the key without deleting the key file, and every change
// lands in the wt-model home — the url in its config.toml (never the key),
// the key verbatim in the 0600 file its key_file names — where ResolveProxy,
// and so every reader, sees it. The legacy [litellm] table of config.toml is
// read-only now and its bytes are never touched.
func TestLitellmSetIndependentAndClear(t *testing.T) {
	legacyPath := litellmLegacyHome(t, false)
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	u, k := "http://localhost:4000", "sk-secret"
	// While the legacy table holds the pair, a one-flag first set would drop
	// the other field (sources never mix), so it is refused, writing nothing.
	if err := runLitellmSet(&bytes.Buffer{}, &u, nil); err == nil || !strings.Contains(err.Error(), "the first set needs both --url and --api-key") {
		t.Fatalf("url-only first set = %v, want the first-set refusal", err)
	}
	if _, err := os.Lstat(config.ProxyHomePath()); !os.IsNotExist(err) {
		t.Fatalf("home config.toml exists after the refused first set: %v", err)
	}
	first := "sk-first"
	if err := runLitellmSet(&bytes.Buffer{}, &u, &first); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u || pair.APIKey != first || pair.Source != config.ProxySourceHome {
		t.Fatalf("after the first set: %+v, want the home pair", pair)
	}
	if err := runLitellmSet(&bytes.Buffer{}, nil, &k); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u || pair.APIKey != k {
		t.Fatalf("key-only set clobbered the url or the key did not land: %+v", pair)
	}
	u2 := "http://other:4000"
	if err := runLitellmSet(&bytes.Buffer{}, &u2, nil); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u2 || pair.APIKey != k {
		t.Fatalf("url-only set clobbered the key: %+v", pair)
	}

	// The home's files: the url in its config.toml and the key verbatim in
	// the 0600 file its key_file names — the key itself never enters a TOML.
	home := config.ProxyHomePath()
	raw, err := os.ReadFile(home)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Litellm struct {
			URL     string `toml:"url"`
			KeyFile string `toml:"key_file"`
		} `toml:"litellm"`
	}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		t.Fatalf("home config.toml does not parse: %v\n%s", err, raw)
	}
	if doc.Litellm.URL != u2 || doc.Litellm.KeyFile != "litellm.key" {
		t.Errorf("home [litellm] = %+v, want url %q and key_file \"litellm.key\"", doc.Litellm, u2)
	}
	if strings.Contains(string(raw), k) {
		t.Errorf("the api key leaked into config.toml:\n%s", raw)
	}
	keyPath := filepath.Join(filepath.Dir(home), "litellm.key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("litellm.key mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, err := os.ReadFile(keyPath); err != nil || string(b) != k {
		t.Errorf("litellm.key = %q (%v), want the key verbatim", b, err)
	}

	empty := ""
	if err := runLitellmSet(&bytes.Buffer{}, nil, &empty); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u2 || pair.APIKey != "" {
		t.Fatalf("--api-key \"\" did not clear (or clobbered the url): %+v", pair)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("clearing the key deleted the key file: %v", err)
	}

	// The legacy table is never written.
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyAfter, legacyBefore) {
		t.Errorf("set rewrote the legacy config.toml:\n%s", legacyAfter)
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
		err := execLitellm(&app{hostSet: true, hostCfg: litellmTestConfig()}, args...)
		if err == nil || !strings.Contains(err.Error(), "wt litellm sync") || !strings.Contains(err.Error(), "removed") {
			t.Errorf("%v: err = %v, want a removal error pointing to `wt litellm sync`", args, err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: []\n" {
		t.Fatalf("removed commands modified config.yaml:\n%s", b)
	}
	var help bytes.Buffer
	c := litellmCmd(&app{hostSet: true, hostCfg: litellmTestConfig()})
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

// TestLitellmStateCommandsFailOnAnUnreadableProxyConfig pins ruling 8: `set`
// is gateless (the escape hatch when a config file is broken) and `status` is
// loud, each failing only on the files its own resolution needs. status fails
// on an unparseable wt-model home always, and on an unparseable legacy
// config.toml only when the home holds no [litellm] table — behind a home
// pair the legacy file is never read. set writes the home over a broken
// legacy file and refuses only when the home itself will not parse (never
// merging onto a document wt cannot read). `set` with no flags stays the one
// usage error and writes nothing. Every failure is one `wt:` line and exit 1
// (M3/C9); `sync`'s load-error refusal is pinned by
// TestLitellmLoadErrorNamesTheRepairThatWorks.
func TestLitellmStateCommandsFailOnAnUnreadableProxyConfig(t *testing.T) {
	const garbage = "this is [not toml\n"
	// newHome is a scratch home (empty registry) plus the two paths the
	// resolution reads: the legacy config.toml and the wt-model home's
	// config.toml.
	newHome := func(t *testing.T) (legacyPath, homePath string) {
		t.Helper()
		dir := t.TempDir()
		writeEmptyRegistry(t, dir)
		return filepath.Join(dir, ".config", "agent-wt", "config.toml"), config.ProxyHomePath()
	}
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("status fails on an unparseable home", func(t *testing.T) {
		_, homePath := newHome(t)
		write(t, homePath, garbage)
		out, code := runRoot(t, "litellm", "status")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, homePath) {
			t.Errorf("exit %d, output %q; want 1 and one error line naming %s", code, out, homePath)
		}
	})
	t.Run("status fails on an unparseable legacy without a home table", func(t *testing.T) {
		legacyPath, _ := newHome(t)
		write(t, legacyPath, garbage)
		out, code := runRoot(t, "litellm", "status")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, "parse") {
			t.Errorf("exit %d, output %q; want 1 and one parse-error line", code, out)
		}
	})
	t.Run("status reads the home pair past an unparseable legacy", func(t *testing.T) {
		legacyPath, homePath := newHome(t)
		write(t, legacyPath, garbage)
		write(t, homePath, "[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"litellm.key\"\n")
		write(t, filepath.Join(filepath.Dir(homePath), "litellm.key"), "sk-home1234")
		out, code := runRoot(t, "litellm", "status")
		if code != 0 || !strings.Contains(out, "url: http://localhost:4000") || !strings.Contains(out, "api_key: ***1234") {
			t.Errorf("exit %d, output %q; want 0 and the home pair's status block", code, out)
		}
	})
	t.Run("set writes the home over an unparseable legacy", func(t *testing.T) {
		legacyPath, _ := newHome(t)
		write(t, legacyPath, garbage)
		out, code := runRoot(t, "litellm", "set", "--url", "http://x:4000", "--api-key", "sk-new1234")
		if code != 0 || strings.TrimSpace(out) != "litellm: updated" {
			t.Errorf("exit %d, output %q; want 0 and the one success line", code, out)
		}
		if pair := resolveProxy(t); pair.URL != "http://x:4000" || pair.APIKey != "sk-new1234" || pair.Source != config.ProxySourceHome {
			t.Fatalf("set did not leave the home pair: %+v", pair)
		}
	})
	t.Run("set fails on an unparseable home and writes nothing", func(t *testing.T) {
		_, homePath := newHome(t)
		write(t, homePath, garbage)
		out, code := runRoot(t, "litellm", "set", "--url", "http://x:4000")
		if code != 1 || len(wtLines(out)) != 1 {
			t.Errorf("exit %d, output %q; want 1 and one error line", code, out)
		}
		if b, _ := os.ReadFile(homePath); string(b) != garbage {
			t.Errorf("set rewrote a home it could not parse:\n%s", b)
		}
	})
	t.Run("set with no flags writes nothing", func(t *testing.T) {
		_, homePath := newHome(t)
		out, code := runRoot(t, "litellm", "set")
		if code != 1 || !strings.Contains(out, "nothing to set: pass --url and/or --api-key") {
			t.Errorf("exit %d, output %q; want 1 and the nothing-to-set message", code, out)
		}
		if _, err := os.Stat(filepath.Dir(homePath)); !os.IsNotExist(err) {
			t.Errorf("no-flag set wrote the config home (%v)", err)
		}
	})
}

// validationOnlyApp builds an app via newApp() in a temp XDG dir whose registry
// has a model pointing at a missing provider: Load succeeds but Validate fails
// (a row whose provider has no row; `wt-model model init` seeds one for a provider
// it has a default for, any other is a hand edit of registry.toml).
func validationOnlyApp(t *testing.T) (*app, string) {
	t.Helper()
	sample, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := string(sample) + "\n[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n"
	registry := modelHome(t, reg)
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	return a, filepath.Join(filepath.Dir(filepath.Dir(registry)), "agent-wt", "config.toml")
}

// TestLitellmRoutingCommandsWorkOnValidationOnlyError pins that status/set
// depend only on the proxy pair (the wt-model home), not on registry
// validity: a model referencing an unknown provider (cfgErr set, loadErr nil)
// must not lock the user out of the only routing control surface, and the
// change must persist where every reader resolves it (ResolveProxy). (sync is
// covered by TestLitellmSyncWorksOnValidationOnlyError.)
func TestLitellmRoutingCommandsWorkOnValidationOnlyError(t *testing.T) {
	litellmEnv(t, "model_list: []\n")
	a, _ := validationOnlyApp(t)
	if a.hostConfigErr() == nil || hostLoadErrOf(a) != nil {
		t.Fatalf("fixture: cfgErr=%v loadErr=%v, want validation-only error", a.hostConfigErr(), hostLoadErrOf(a))
	}
	for _, args := range [][]string{{"set", "--url", "http://localhost:4000", "--api-key", "sk-abcdef"}, {"status"}} {
		if err := execLitellm(a, args...); err != nil {
			t.Fatalf("%v refused on validation-only error: %v", args, err)
		}
	}
	if pair := resolveProxy(t); pair.URL != "http://localhost:4000" || pair.APIKey != "sk-abcdef" || pair.Source != config.ProxySourceHome {
		t.Fatalf("state not persisted where every reader resolves it: %+v", pair)
	}
}

// TestLitellmSyncWorksOnValidationOnlyError pins that sync gates only on a
// config LOAD failure. A registry gap in an unrelated model (unknown
// provider: cfgErr set, loadErr nil) must not block the sync: one bad row
// would otherwise leave every other model's route stale until the registry
// was fixed. The running healthy model is still routed, and the broken model
// gets no route.
func TestLitellmSyncWorksOnValidationOnlyError(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	a, _ := validationOnlyApp(t)
	if a.hostConfigErr() == nil || hostLoadErrOf(a) != nil {
		t.Fatalf("fixture: cfgErr=%v loadErr=%v", a.hostConfigErr(), hostLoadErrOf(a))
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
	if err := execLitellm(a, "sync"); err != nil {
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
		if err := execLitellm(&app{hostSet: true, hostCfg: &config.Config{}, hostLoadErr: errors.New("bad toml")}, args...); err == nil || !strings.Contains(err.Error(), "config error: bad toml") {
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
// is printed as one JSON document (dry_run true, the plan's id lists) and
// config.yaml is not written.
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
// document against the fixture's sync_dry_run block
// (docs/contracts/litellm-cli.sample.json). No program in this repository
// reads that block — nothing here calls `wt litellm sync --dry-run --json` —
// so this is not guarding a live consumer. It is guarding the fixture itself:
// the block is the documented shape of the command's output, and without
// this pin a field added to litellm.SyncPlanJSON (or to its nested plan) would
// silently leave the fixture describing a shape wt no longer emits, for
// whoever reads it next. A field added to litellm.SyncPlanJSON must reach the fixture
// in the same change.
func TestLitellmSyncDryRunJSONMatchesContract(t *testing.T) {
	plan := litellm.SyncPlan{
		Add:    []string{"openrouter/x/y", "ollama/llama3.2:3b"},
		Adopt:  []string{"ollama/gemma:9b"},
		Remove: []string{"openrouter/old"},
		Repair: []string{"ollama/mine"},
		Errors: []litellm.Outcome{{ID: "ghost/m", Err: errors.New(`unknown provider "ghost"`)}},
	}
	var out, errOut bytes.Buffer
	// No probe warnings in this rendering; a plan with errors exits 1, like a
	// real sync.
	if err := litellm.ReportSyncPlan(&out, &errOut, plan, nil, true); !errors.Is(err, litellm.ErrIDFailed) {
		t.Fatalf("litellm.ReportSyncPlan err = %v, want litellm.ErrIDFailed", err)
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
	// Pin the key sets too, so a field that is added to litellm.SyncPlanJSON but left
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
// warnings array. The fixture (docs/contracts/litellm-cli.sample.json) is
// read by this test: a script that parses `wt litellm sync --json` breaks if
// a key or an action name changes.
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
  - model_name: ollama/mine
    litellm_params: {model: ollama_chat/mine}
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
	rows := routeRows(t, p)
	if !slices.Contains(rows, litellm.RowInfo{ID: "ollama/llama3.2:3b", Managed: true}) {
		t.Fatalf("rows = %v, want the discovered route written marked", rows)
	}
	var out, errOut bytes.Buffer
	if err := litellm.Report(&out, &errOut, res, true); !errors.Is(err, litellm.ErrIDFailed) {
		t.Fatalf("litellm.Report err = %v, want litellm.ErrIDFailed", err)
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

// TestLitellmListJSONRows pins list's ownership rows next to the older
// "routed" id list, which stays for scripts that already read it.
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
			[]string{downProbeWarn}},
		// #195: a family whose only route is a discovered model's marked row
		// has no registry local model to notice, so the row was removed with
		// no word. It is a local route treated as stale like any other.
		{"only a discovered route", "model_list:\n  - model_name: ollama/pulled:1b\n    litellm_params: {model: ollama_chat/pulled:1b}\n    model_info: {wt_managed: true}\n", func() *config.Config {
			cfg := litellmTestConfig()
			cfg.Models = cfg.Models[1:] // drop the registry ollama model: nothing local is registered
			return cfg
		}, []string{downProbeWarn}},
		// A hand-written row of the family is not wt's to remove, so a refused
		// daemon says nothing about it.
		{"only a hand-written row", "model_list:\n  - model_name: ollama/mine:1b\n    litellm_params: {model: ollama_chat/mine:1b}\n", func() *config.Config {
			cfg := litellmTestConfig()
			cfg.Models = cfg.Models[1:]
			return cfg
		}, nil},
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
	want := []litellm.RowInfo{{ID: "omlx/stray-model", Managed: true}, {ID: "ollama/llama3.2:3b", Managed: true}, {ID: "mtplx/org/Qwen3.8-27B", Managed: true}}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestLitellmSyncDropsDiscoveredEntryCollidingWithRegistryID pins the
// command-level half of the id-collision rule: the registry model
// "ollama/foo" serves model_name "bar" and is not pulled, while an
// unregistered pulled artifact "foo" carries the same discovered id. The
// registry model owns the id, so the colliding entry is in neither the
// desired models nor the desired ids the under-lock Recheck returns, and sync
// — dry run and real — leaves no "ollama/foo → ollama_chat/bar" route to a
// model that is not on disk, removing one written before.
func TestLitellmSyncDropsDiscoveredEntryCollidingWithRegistryID(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: ollama/foo
    litellm_params: {model: ollama_chat/bar, api_base: http://localhost:11434}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Models = append(cfg.Models, config.Model{ID: "ollama/foo", ProviderID: "ollama", ModelName: "bar", Location: config.LocationLocal})
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/foo", ModelName: "bar", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/foo", ModelName: "foo", Artifact: "foo", ArtifactKnown: true},
		},
	}
	if got := recheckIDs(cfg, snap); len(got) != 0 {
		t.Fatalf("desiredLocalIDs = %v, want none", got)
	}
	if got := desiredModels(cfg, snap); len(got) != 0 {
		t.Fatalf("desiredLocalModels = %+v, want none", got)
	}
	stubProbeInventory(t, snap)
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
		t.Fatalf("dry run: %v (stderr %q)", err, errOut.String())
	}
	var dry litellm.SyncPlanJSON
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
		t.Fatalf("dry run: not JSON: %q", out.String())
	}
	if !slices.Equal(dry.Plan.Remove, []string{"ollama/foo"}) || len(dry.Plan.Add) != 0 {
		t.Fatalf("dry-run plan = %+v, want only ollama/foo removed", dry.Plan)
	}
	out.Reset()
	if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	if got := routeRows(t, p); len(got) != 0 {
		t.Fatalf("rows = %v, want the ollama/foo route gone", got)
	}
}

// TestStartHookAndSyncAgreeOnDiscoveredRoute pins, end to end, that the
// start hook and `wt litellm sync` name a discovered model's route the same
// way. The picker row is built by localmodels.CatalogRows from the snapshot's
// discovered omlx entry; the start paths hand the hook
// Target{row.Model.ProviderID, row.Model.ModelName, row.Model.ID}; the hook's change
// (lifecycle.StartRouteChange — the derivation routeAfterStart writes) goes
// through the real litellm.ApplyChange; then sync runs over the same
// snapshot. Its dry run must plan nothing, and the real run must report
// changed == false and restart nothing: were the
// two ids to differ, every sync after a start would remove the hook's route
// and add its own, bouncing the proxy under the agent just launched.
func TestStartHookAndSyncAgreeOnDiscoveredRoute(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	restarts := filepath.Join(t.TempDir(), "restarts")
	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: config.DiscoveredModelID("omlx", "org/New-Model-4bit"), ModelName: "org/New-Model-4bit", Artifact: "org/New-Model-4bit", ArtifactKnown: true, Running: true},
		},
	}
	rows := localmodels.CatalogRows(localmodels.CatalogInput{Config: cfg, Inventory: &snap})
	row, ok := catalog.Find(rows, "omlx/org/New-Model-4bit")
	if !ok || !row.Discovered {
		t.Fatalf("no discovered row for the omlx entry in %+v", rows)
	}
	ch := lifecycle.StartRouteChange(cfg, lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID})
	// NoRestart, as the hook's own write passes: the hook bounces the proxy
	// itself, so any restart counted below is sync's.
	res, err := litellm.ApplyChange(cfg, ch, litellm.Options{NoRestart: true})
	if err != nil || !res.Changed {
		t.Fatalf("hook write: changed=%v err=%v, want the route written", res.Changed, err)
	}
	want := []litellm.RowInfo{{ID: row.Model.ID, Managed: true}}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows after the hook = %v, want %v", got, want)
	}

	stubProbeInventory(t, snap)
	var out, errOut bytes.Buffer
	// The dry run first: it has no under-lock Recheck, so it shows the plan
	// built from desiredLocalModels alone.
	if err := runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
		t.Fatalf("dry run: %v (stderr %q)", err, errOut.String())
	}
	var dry litellm.SyncPlanJSON
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
		t.Fatalf("dry run: not JSON: %q", out.String())
	}
	if len(dry.Plan.Add)+len(dry.Plan.Remove) != 0 {
		t.Errorf("dry run after the hook plans add %v remove %v, want nothing", dry.Plan.Add, dry.Plan.Remove)
	}
	out.Reset()
	if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	var doc litellm.ResultJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("sync: not JSON: %q", out.String())
	}
	if doc.Changed || len(doc.Outcomes) != 0 {
		t.Errorf("sync after the hook: changed=%v outcomes=%+v, want the hook's route left exactly as written", doc.Changed, doc.Outcomes)
	}
	if b, err := os.ReadFile(restarts); err == nil {
		t.Errorf("sync restarted the proxy: %q", b)
	}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Errorf("rows after sync = %v, want %v", got, want)
	}
}

// TestEnsureRouteChangeIsIdempotent pins "a launch whose route already exists
// changes nothing" against the real litellm.ApplyChange: the Add-only change
// the launch-time route check writes (lifecycle.EnsureRoute, #192) reports
// Changed on the first write and not on the second, which leaves config.yaml
// byte-for-byte as it was. Changed is what triggers the proxy restart, so a
// second write that reported a change would bounce the proxy on every launch.
func TestEnsureRouteChangeIsIdempotent(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	cfg := litellmTestConfig()
	target := lifecycle.Target{ProviderID: "ollama", ModelName: "gemma:9b", ModelID: "ollama/gemma:9b"}
	ch := litellm.Change{Add: lifecycle.StartRouteChange(cfg, target).Add}

	res, err := litellm.ApplyChange(cfg, ch, litellm.Options{NoRestart: true})
	if err != nil || !res.Changed {
		t.Fatalf("first write: changed=%v err=%v, want the route written", res.Changed, err)
	}
	want := []litellm.RowInfo{{ID: "ollama/gemma:9b", Managed: true}}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows after the first write = %v, want %v", got, want)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	res, err = litellm.ApplyChange(cfg, ch, litellm.Options{NoRestart: true})
	if err != nil || res.Changed {
		t.Fatalf("second write: changed=%v err=%v, want nothing changed", res.Changed, err)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("config.yaml changed on the second write:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Errorf("rows after the second write = %v, want %v", got, want)
	}
}

// TestLitellmSyncRemovesRoutesOfUnprobedFamily pins that a family the
// inventory never probed — the registry has no mtplx provider row and no
// mtplx model, so Snapshot.Providers has no mtplx key — is not frozen: its
// leftover marked route (a discovered route written before the provider was
// dropped from the registry) is removed, with no "probe did not succeed"
// warning, by the dry run and the real sync alike. Treating "never probed" as
// "untrusted" kept that route, and the warning, on every sync forever. The
// unmarked row of the same shape is hand-written and survives.
func TestLitellmSyncRemovesRoutesOfUnprobedFamily(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: mtplx/org/gone
    litellm_params: {model: openai/org/gone, api_base: http://localhost:8003/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: mtplx/org/by-hand
    litellm_params: {model: openai/org/by-hand, api_base: http://localhost:8003/v1, api_key: not-needed}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig() // ollama only: nothing names mtplx
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
		t.Fatalf("dry run: %v (stderr %q)", err, errOut.String())
	}
	var dry litellm.SyncPlanJSON
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
		t.Fatalf("dry run: not JSON: %q", out.String())
	}
	if !slices.Equal(dry.Plan.Remove, []string{"mtplx/org/gone"}) || len(dry.Plan.Add) != 0 || len(dry.Warnings) != 0 {
		t.Fatalf("dry run = %+v, want only mtplx/org/gone removed and no warnings", dry)
	}
	out.Reset()
	if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	var real litellm.ResultJSON
	if err := json.Unmarshal(out.Bytes(), &real); err != nil {
		t.Fatalf("sync: not JSON: %q", out.String())
	}
	if len(real.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", real.Warnings)
	}
	if got := routeRows(t, p); !slices.Equal(got, []litellm.RowInfo{{ID: "mtplx/org/by-hand"}}) {
		t.Fatalf("rows = %v, want only the hand-written mtplx/org/by-hand", got)
	}
}

// TestLitellmSyncKeepsRoutesOfRegistryGapFamily pins that a family the
// inventory could not probe because of a registry data gap stays frozen. The
// registry still names omlx — a provider row with no location, or (after
// `providers = []` landed) a model whose provider is gone — so nothing was
// probed and Snapshot.Providers has no omlx key, yet that says nothing about
// whether the server is up. A live marked discovered route (omlx/org/live)
// must be kept, exactly as planSync keeps the gap model's own row
// (omlx/reg), with a warning that names the real cause, identical in the dry
// run and the real run, and with no proxy restart. Treating the gap as
// "never probed, so stale" removed a serving model's route silently.
func TestLitellmSyncKeepsRoutesOfRegistryGapFamily(t *testing.T) {
	const body = `model_list:
  - model_name: omlx/org/live
    litellm_params: {model: openai/org/live, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
  - model_name: omlx/reg
    litellm_params: {model: openai/reg, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
general_settings:
  cancel_on_disconnect: true
`
	reg := config.Model{ID: "omlx/reg", ProviderID: "omlx", ModelName: "reg"}
	noLocation := litellmTestConfig()
	noLocation.Providers = append(noLocation.Providers, config.Provider{ID: "omlx", Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	noLocation.Models = append(noLocation.Models, reg)
	noProviders := litellmTestConfig()
	noProviders.Providers = nil
	noProviders.Models = append(noProviders.Models, reg)
	// A location that is neither "local" nor "cloud" (a typo such as "Local")
	// is the same kind of gap (#195): the inventory probes only an exact
	// "local", so the family went unprobed, and because the gap check looked
	// only for an EMPTY location the family was not frozen either — sync
	// removed a serving model's route with no warning at all.
	badLocation := litellmTestConfig()
	badLocation.Providers = append(badLocation.Providers, config.Provider{ID: "omlx", Location: "Local", Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	badLocation.Models = append(badLocation.Models, reg)
	// The typo can sit on the model instead: its provider row is sound (and
	// not local, so the family still goes unprobed), and the model's own
	// location override is the gap to repair.
	badModelLocation := litellmTestConfig()
	badModelLocation.Providers = append(badModelLocation.Providers, config.Provider{ID: "omlx", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	badModelLocation.Models = append(badModelLocation.Models, config.Model{ID: "omlx/reg", ProviderID: "omlx", ModelName: "reg", Location: "Local"})
	// Each cause is named in its own words (#195): "no resolvable location"
	// sent a user whose provider row was simply missing to look for a
	// location to fix.
	const tail = "; its model routes were left unchanged"
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want string
		// also is a further warning the case earns: with no provider rows at
		// all, ollama's model has no row either, and no probe line says so.
		also []string
	}{
		{"provider row without a location", noLocation, `provider "omlx" could not be probed (its registry entry has no location)` + tail, nil},
		{"providers empty", noProviders, `provider "omlx" could not be probed (the registry has models for it but no provider entry)` + tail,
			[]string{`provider "ollama" has no [[providers]] row in ` + config.RegistryPath() + ", so wt cannot route ollama/gemma:9b; add the row (`wt-model model init` adds the default ones) or fix the provider_id"}},
		{"provider row with a mistyped location", badLocation, `provider "omlx" could not be probed (its registry entry has location "Local"; expected "local" or "cloud")` + tail, nil},
		{"model with a mistyped location of its own", badModelLocation, `provider "omlx" could not be probed (model "omlx/reg" has location "Local"; expected "local" or "cloud")` + tail, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := append([]string{tc.want}, tc.also...)
			p := litellmEnv(t, body)
			restarts := filepath.Join(t.TempDir(), "restarts")
			t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
			stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
			var out, errOut bytes.Buffer
			if err := runLitellmSync(&out, &errOut, tc.cfg, true, true); err != nil {
				t.Fatalf("dry run: %v (stderr %q)", err, errOut.String())
			}
			var dry litellm.SyncPlanJSON
			if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
				t.Fatalf("dry run: not JSON: %q", out.String())
			}
			if len(dry.Plan.Add)+len(dry.Plan.Remove) != 0 || !slices.Equal(dry.Warnings, want) {
				t.Errorf("dry run: add %v remove %v warnings %q; want no change and only %q", dry.Plan.Add, dry.Plan.Remove, dry.Warnings, want)
			}
			out.Reset()
			if err := runLitellmSync(&out, &errOut, tc.cfg, true, false); err != nil {
				t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
			}
			var real litellm.ResultJSON
			if err := json.Unmarshal(out.Bytes(), &real); err != nil {
				t.Fatalf("sync: not JSON: %q", out.String())
			}
			if real.Changed || !slices.Equal(real.Warnings, dry.Warnings) {
				t.Errorf("sync: changed=%v warnings %q; want unchanged and the dry run's warnings %q", real.Changed, real.Warnings, dry.Warnings)
			}
			if b, err := os.ReadFile(restarts); err == nil {
				t.Errorf("sync restarted the proxy: %q", b)
			}
			if got, wantRows := routeRows(t, p), []litellm.RowInfo{{ID: "omlx/org/live", Managed: true}, {ID: "omlx/reg", Managed: true}}; !slices.Equal(got, wantRows) {
				t.Errorf("rows = %v, want %v", got, wantRows)
			}
		})
	}
}

// TestLitellmSyncWarnsWhenAGapFamilyHasOnlyRegistryRoutes pins that the
// registry-gap warning does not depend on a discovered route being present
// (#195). The usual shape of the gap is a provider row with a missing or
// mistyped location and nothing in config.yaml but its registry models' own
// routes. Those rows are kept — and RowFamily answers "" for them, since
// their location does not resolve to "local" — so the family was frozen with
// no warning at all: a route that never follows a start or a stop again, and
// nothing telling the user to repair the registry.
func TestLitellmSyncWarnsWhenAGapFamilyHasOnlyRegistryRoutes(t *testing.T) {
	const body = `model_list:
  - model_name: omlx/reg
    litellm_params: {model: openai/reg, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`
	const tail = "; its model routes were left unchanged"
	for _, tc := range []struct {
		name     string
		location config.Location
		want     string
	}{
		{"no location", "", `provider "omlx" could not be probed (its registry entry has no location)` + tail},
		{"mistyped location", "Local", `provider "omlx" could not be probed (its registry entry has location "Local"; expected "local" or "cloud")` + tail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := litellmEnv(t, body)
			cfg := litellmTestConfig()
			cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx", Location: tc.location, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
			cfg.Models = append(cfg.Models, config.Model{ID: "omlx/reg", ProviderID: "omlx", ModelName: "reg"})
			stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
			for _, dryRun := range []bool{true, false} {
				var out, errOut bytes.Buffer
				if err := runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
					t.Fatalf("dryRun=%v: %v (stderr %q)", dryRun, err, errOut.String())
				}
				var doc struct{ Warnings []string }
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil || !slices.Equal(doc.Warnings, []string{tc.want}) {
					t.Errorf("dryRun=%v: stdout %q (%v), want only the warning %q", dryRun, out.String(), err, tc.want)
				}
			}
			if got, want := routeRows(t, p), []litellm.RowInfo{{ID: "omlx/reg", Managed: true}}; !slices.Equal(got, want) {
				t.Errorf("rows = %v, want the gap model's route kept %v", got, want)
			}
		})
	}
}

// TestLitellmSyncRemovesRoutesOfNonLocalFamily pins the other side of the
// registry-gap rule: a family the registry references only as explicitly
// non-local — an omlx provider row with location "cloud" and no local model —
// was not probed for a reason that is not a data gap, so it is not frozen.
// Its leftover marked route is removed without a warning; freezing it would
// strand that route forever, since no probe will ever run for the family.
func TestLitellmSyncRemovesRoutesOfNonLocalFamily(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: omlx/org/gone
    litellm_params: {model: openai/org/gone, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "none", BaseURL: "http://remote:8000"}})
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
			t.Fatalf("dryRun=%v: %v (stderr %q)", dryRun, err, errOut.String())
		}
		var doc struct{ Warnings []string }
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil || len(doc.Warnings) != 0 {
			t.Errorf("dryRun=%v: stdout %q (%v), want no warnings", dryRun, out.String(), err)
		}
	}
	if got := routeRows(t, p); len(got) != 0 {
		t.Fatalf("rows = %v, want the stale omlx route removed", got)
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
	var dry litellm.SyncPlanJSON
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
	var real litellm.ResultJSON
	if err := json.Unmarshal(out.Bytes(), &real); err != nil {
		t.Fatalf("sync: not JSON: %q", out.String())
	}
	var added, removed, repaired []string
	for _, oc := range real.Outcomes {
		switch oc.Action {
		case "routed":
			added = append(added, oc.ID)
		case "unrouted":
			removed = append(removed, oc.ID)
		case "api_base set":
			repaired = append(repaired, oc.ID)
		default:
			t.Errorf("unexpected outcome %+v", oc)
		}
	}
	if !slices.Equal(added, dry.Plan.Add) || !slices.Equal(removed, dry.Plan.Remove) {
		t.Errorf("real sync routed %v unrouted %v, dry run planned add %v remove %v", added, removed, dry.Plan.Add, dry.Plan.Remove)
	}
	// The hand-written ollama/q8 row has no api_base: the write fills it
	// (#202), and the dry run must have said so.
	if want := []string{"ollama/q8"}; !slices.Equal(repaired, want) || !slices.Equal(dry.Plan.Repair, want) {
		t.Errorf("real sync set api_base on %v, dry run planned %v; want %v for both", repaired, dry.Plan.Repair, want)
	}
	if !slices.Equal(real.Warnings, dry.Warnings) || len(dry.Warnings) != 1 {
		t.Errorf("warnings: real %q, dry run %q; want the same single omlx probe warning", real.Warnings, dry.Warnings)
	}
	want := []litellm.RowInfo{{ID: "omlx/stray-model", Managed: true}, {ID: "ollama/q8"}, {ID: "ollama/llama3.2:3b", Managed: true}}
	if got := routeRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestSyncAndStartHookAgreeWhenOmlxListsAnUnloadedSibling pins the fix for
// #201, end to end through the real inventory and a server that answers as
// omlx does: /v1/models lists every model in its pool, loaded or not. wt read
// that list as "running", so sync routed both models, the start hook — which
// treats omlx as holding one model — removed the sibling's route, and the next
// sync put it back: one config.yaml write and proxy restart each time. The
// probe now counts only what omlx has loaded, so sync routes the loaded model
// alone and the start hook has nothing to undo.
//
// What is left, deliberately: omlx can hold two models loaded at once, and
// then sync (routes both) and the start hook (keeps one) still differ. wt
// itself never produces that state — it replaces the occupant — so it needs a
// second model loaded behind wt's back.
func TestSyncAndStartHookAgreeWhenOmlxListsAnUnloadedSibling(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	dir := t.TempDir()
	for _, m := range []string{"A-4bit", "B-4bit"} {
		if err := os.MkdirAll(filepath.Join(dir, m), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, m, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"A-4bit"},{"id":"B-4bit"}]}`)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"healthy","engine_pool":{"model_count":2,"loaded_count":1}}`)
	})
	mux.HandleFunc("/v1/models/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"id":"A-4bit","loaded":true},{"id":"B-4bit","loaded":false}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	omlx := config.Provider{ID: "omlx", Location: config.LocationLocal, ModelDir: dir, Auth: config.AuthConfig{Type: "none", BaseURL: srv.URL}}
	// The real probe, against the omlx provider alone (the test config's
	// ollama provider would be dialed for real).
	snap := localmodels.Inventory(&config.Config{Providers: []config.Provider{omlx}})
	if snap.Providers["omlx"] != localmodels.StatusOK || len(snap.Entries) != 2 {
		t.Fatalf("inventory = %+v, want omlx ok with both models on disk", snap)
	}
	snap.Providers["ollama"] = localmodels.StatusOK
	stubProbeInventory(t, snap)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, omlx)
	rows := func() []string {
		t.Helper()
		var ids []string
		for _, r := range routeRows(t, p) {
			ids = append(ids, r.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sync := func() bool {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
			t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
		}
		var doc struct {
			Changed bool `json:"changed"`
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out.String())
		}
		return doc.Changed
	}
	loaded := []string{"omlx/A-4bit"}

	sync()
	if got := rows(); !slices.Equal(got, loaded) {
		t.Fatalf("after sync rows = %v, want only the loaded model %v: a listed, unloaded sibling is not running", got, loaded)
	}
	ch := lifecycle.StartRouteChange(cfg, lifecycle.Target{ProviderID: "omlx", ModelName: "A-4bit", ModelID: "omlx/A-4bit"})
	res, err := litellm.ApplyChange(cfg, ch, litellm.Options{NoRestart: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows(); res.Changed || !slices.Equal(got, loaded) {
		t.Fatalf("after the start hook changed=%v rows = %v, want nothing to undo and %v", res.Changed, got, loaded)
	}
	if sync() {
		t.Fatalf("the next sync rewrote config.yaml (rows %v); sync and the start hook must agree", rows())
	}
}

// TestSyncReportsTheAPIBaseRepairInText pins the human-readable lines for the
// api_base repair (#202): a dry run says which rows the write would change,
// and the real sync says which it changed — these can be rows the user wrote
// by hand, so the change must be visible both before and after.
func TestSyncReportsTheAPIBaseRepairInText(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := litellm.ReportSyncPlan(&out, &errOut, litellm.SyncPlan{Repair: []string{"ollama/q8"}}, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "ollama/q8: would set api_base\n") {
		t.Errorf("dry run printed %q, want the would-set line", got)
	}
	out.Reset()
	res := litellm.Result{Outcomes: []litellm.Outcome{{ID: "ollama/q8", Action: "api_base set"}}, Changed: true}
	if err := litellm.Report(&out, &errOut, res, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "ollama/q8: api_base set\n" {
		t.Errorf("sync printed %q, want %q", got, "ollama/q8: api_base set\n")
	}
}

// TestLitellmSyncWarnsAboutAnOllamaServeRow pins #206 at the command: `wt
// litellm sync` and its dry run both name a hand-written row LiteLLM starts
// its own `ollama serve` for and wt cannot repair (the word ollama in a model
// that is not an ollama/ one, no api_base), in JSON `warnings` and as a
// stderr `warning:` line. The dry run must say it too, or it promises a clean
// run the real sync does not deliver; neither changes the row or fails.
func TestLitellmSyncWarnsAboutAnOllamaServeRow(t *testing.T) {
	body := "model_list:\n  - model_name: hand/proxy\n    litellm_params:\n      model: openai/ollama-proxy\n"
	p := litellmEnv(t, body)
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	const want = `row "hand/proxy" (model openai/ollama-proxy) has no api_base: LiteLLM starts its own "ollama serve" for it at proxy startup; give the row an api_base`
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, dryRun); err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		var doc struct {
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("dryRun=%v: not JSON: %v\n%s", dryRun, err, out.String())
		}
		if !slices.Equal(doc.Warnings, []string{want}) {
			t.Errorf("dryRun=%v: warnings = %q, want [%q]", dryRun, doc.Warnings, want)
		}
		out.Reset()
		errOut.Reset()
		if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), false, dryRun); err != nil {
			t.Fatalf("dryRun=%v text: %v", dryRun, err)
		}
		if got := errOut.String(); got != "warning: "+want+"\n" {
			t.Errorf("dryRun=%v: stderr = %q, want the warning line", dryRun, got)
		}
	}
	if after, _ := os.ReadFile(p); !strings.Contains(string(after), "model: openai/ollama-proxy\n") || strings.Contains(string(after), "hand/proxy\n    litellm_params:\n      model: openai/ollama-proxy\n      api_base") {
		t.Errorf("sync changed the row it only warns about:\n%s", after)
	}
}

// TestLitellmSyncWarnsAboutAProviderWithNoRow verifies sync names a provider
// id that models reference and no [[providers]] row defines, once, with the
// models, in the real run and the dry run alike. The model is neither routed
// nor (if it had a route) unrouted, and without this line the user sees a
// registry model with no route and no explanation.
func TestLitellmSyncWarnsAboutAProviderWithNoRow(t *testing.T) {
	cfg := litellmTestConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "corp/a", ProviderID: "corp", ModelName: "a", Location: config.LocationCloud},
		config.Model{ID: "corp/b", ProviderID: "corp", ModelName: "b"},
	)
	want := `provider "corp" has no [[providers]] row in ` + config.RegistryPath() + ", so wt cannot route corp/a, corp/b; add the row (`wt-model model init` adds the default ones) or fix the provider_id"
	for _, dryRun := range []bool{true, false} {
		litellmEnv(t, "model_list: []\n")
		stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, cfg, false, dryRun); err != nil {
			t.Fatalf("dry run %v: %v", dryRun, err)
		}
		if got := errOut.String(); got != "warning: "+want+"\n" {
			t.Errorf("dry run %v: stderr = %q\nwant the one warning %q", dryRun, got, want)
		}
		if strings.Contains(out.String(), "corp/") {
			t.Errorf("dry run %v: a model with no provider row was routed:\n%s", dryRun, out.String())
		}
	}
	// A registry with no gap says nothing.
	litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil || errOut.Len() != 0 {
		t.Errorf("no gap: err = %v, stderr = %q; want silence", err, errOut.String())
	}
}

// litellmLegacyHome is a scratch config home whose config.toml holds the
// retired [litellm] enabled key beside a url and key, and an empty registry.
// It returns the config.toml path.
func litellmLegacyHome(t *testing.T, enabled bool) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	p := filepath.Join(home, ".config", "agent-wt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("default_tag = \"code\"\n\n[litellm]\nenabled = %v\nurl = \"http://localhost:4000\"\napi_key = \"sk-host1234\"\n", enabled)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLitellmOnOffAreRetired pins the retirement of `wt litellm on|off`
// (Task 18, C5): each is a hidden stub, dated 2026-11-15, that exits 1 with
// one line saying every session routes through the model server and naming
// the [model-server] settings, and changes nothing in config.toml. Left
// working, `on` would flip a key nothing reads and let a user believe they had
// re-routed their agents; removed outright, cobra would print help and exit 0.
func TestLitellmOnOffAreRetired(t *testing.T) {
	for _, word := range []string{"on", "off"} {
		t.Run(word, func(t *testing.T) {
			p := litellmLegacyHome(t, false)
			before, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			out, code := runRoot(t, "litellm", word)
			want := "wt: litellm " + word + " is retired — every session routes through the model server; set url and token_file under [model-server] in config.toml; nothing was changed\n"
			if code != 1 || !strings.HasSuffix(out, want) || len(wtLines(out)) != 1 {
				t.Errorf("exit %d, output %q; want 1 and %q", code, out, want)
			}
			if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
				t.Errorf("config.toml changed:\n%s", after)
			}
		})
	}
	c := litellmCmd(&app{})
	for _, sub := range c.Commands() {
		if sub.Name() != "on" && sub.Name() != "off" {
			continue
		}
		if !sub.Hidden || sub.Annotations[removedCmdAnnotation] != "2026-11-15" {
			t.Errorf("litellm %s: hidden=%v annotation=%q, want hidden and dated 2026-11-15", sub.Name(), sub.Hidden, sub.Annotations[removedCmdAnnotation])
		}
	}
}

// TestLitellmStatusHasNoEnabledKey pins `wt litellm status` after the flip:
// its JSON is exactly {url, api_key_set} (the fixture's status member, which
// lost enabled), the text names no on/off mode, and the key itself is never
// printed. enabled no longer decides anything, so reporting it would tell a
// script that routing could be switched off. A pair the legacy [litellm]
// fallback supplies prints the one note on stderr (ruling 7), never on stdout.
func TestLitellmStatusHasNoEnabledKey(t *testing.T) {
	litellmLegacyHome(t, true)
	var out, errOut bytes.Buffer
	if err := runLitellmStatus(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("status --json = %q: %v", out.String(), err)
	}
	if want := map[string]any{"url": "http://localhost:4000", "api_key_set": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("status --json = %v, want %v", got, want)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if gotKeys, wantKeys := jsonKeySet(t, out.Bytes()), jsonKeySet(t, fixture["status"]); !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("status keys = %v, fixture = %v", gotKeys, wantKeys)
	}
	out.Reset()
	errOut.Reset()
	if err := runLitellmStatus(&out, &errOut, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "litellm: on") || strings.Contains(s, "litellm: off") || strings.Contains(s, "sk-host1234") || !strings.Contains(s, "url: http://localhost:4000") {
		t.Errorf("status text = %q, want the url, no mode and no key", s)
	}
	if want := config.ProxyFallbackNote() + "\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want the one fallback note %q", errOut.String(), want)
	}
}

// TestLitellmEnabledNoteOncePerProcess pins the one note for the retired key:
// a config.toml with [litellm] enabled = true prints `wt: note: config.toml:
// [litellm] enabled is no longer used and is ignored` once per process, however
// many commands the process builds, and a file without it prints nothing.
// Printed on every command it would bury real output; never printed, a user
// would not learn their toggle stopped doing anything.
func TestLitellmEnabledNoteOncePerProcess(t *testing.T) {
	const note = "wt: note: config.toml: [litellm] enabled is no longer used and is ignored\n"
	resetIgnoredLitellmNote(t)
	litellmLegacyHome(t, false)
	errs := captureStderr(t)
	if _, err := newApp(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errs.String(), "[litellm] enabled") {
		t.Errorf("enabled = false noted: %q", errs.String())
	}
	litellmLegacyHome(t, true)
	for range 3 {
		if _, err := newApp(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(errs.String(), note); n != 1 {
		t.Errorf("note printed %d times, want once: %q", n, errs.String())
	}
}

// resetIgnoredLitellmNote forgets, for this test, that the process already
// printed the [litellm] enabled note.
func resetIgnoredLitellmNote(t *testing.T) {
	t.Helper()
	old := ignoredLitellmNoted.Load()
	ignoredLitellmNoted.Store(false)
	t.Cleanup(func() { ignoredLitellmNoted.Store(old) })
}

// TestLitellmSetRefusesAnAtURL pins the write side of #640 on the `wt
// litellm set` twin: a url with an '@' anywhere exits 1 with one `wt:` line,
// the fixed refusal, quoting no piece of the url, and writes no wt-model
// home, even with --api-key beside it.
func TestLitellmSetRefusesAnAtURL(t *testing.T) {
	for _, raw := range []string{"USERY:PASSY@litellm:4000", "http://USERY:PASSY@localhost:4000"} {
		dir := t.TempDir()
		writeEmptyRegistry(t, dir)
		out, code := runRoot(t, "litellm", "set", "--url", raw, "--api-key", "sk-new1234")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, config.ErrProxyURLHasAt.Error()) {
			t.Errorf("%q: exit %d, output %q; want 1 and the one fixed refusal line", raw, code, out)
		}
		if strings.Contains(out, "USERY") || strings.Contains(out, "PASSY") {
			t.Errorf("%q: the output quotes the url: %q", raw, out)
		}
		if _, err := os.Stat(filepath.Dir(config.ProxyHomePath())); !os.IsNotExist(err) {
			t.Errorf("%q: the wt-model home was written (%v)", raw, err)
		}
	}
}
