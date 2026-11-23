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

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

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

// TestLitellmSyncUsesLiveInventory pins that sync derives "running" from the
// live inventory (never a stored flag): a running registered model gets a
// route and an unrouted-but-stopped one keeps none.
func TestLitellmSyncUsesLiveInventory(t *testing.T) {
	h := testHost()
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: true},
		}})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "model_name: 'ollama/gemma:9b'") {
		t.Fatalf("running model not routed:\n%s", b)
	}
}

// TestLitellmSyncWaitsForTheProxy pins #379 at the command: `wt-model litellm
// sync` (which `wt-model cloud-sync`, `wt-model model add|edit|rm|init` and the Models tab
// run after a registry write) waited for nothing after its restart, so it
// returned while the proxy still refused connections. With a proxy listening
// at the [litellm] url, the restart — the WT_LITELLM_RESTART_CMD override
// here, which the lifecycle path waits after too — is followed by the wait,
// before the command returns, and a proxy that never answers is a warning on
// stderr, not a failed sync.
func TestLitellmSyncWaitsForTheProxy(t *testing.T) {
	h := testHost()
	p := litellmEnv(t, "model_list: []\n")
	restarts := filepath.Join(t.TempDir(), "restarts")
	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
	stubProbeInventory(h, localmodels.Snapshot{
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
	if err := h.runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
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

// TestLitellmSyncRefusesOnConfigError pins that sync refuses when the
// registry failed to LOAD, writing nothing: acting on an empty registry
// would still write config.yaml and restart the proxy. (Validation-only
// errors are covered by TestLitellmSyncWorksOnValidationOnlyError.)
func TestLitellmSyncRefusesOnConfigError(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: x}\n"
	modelHome(t, "models = [\n")
	p := litellmEnv(t, body)
	h := testHost()
	_, _, err := execWTModel(t, h, "litellm", "sync")
	if err == nil || !strings.HasPrefix(err.Error(), "config error: ") {
		t.Fatalf("err = %v, want a config error refusal", err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("sync modified config.yaml:\n%s", b)
	}
}

// TestLitellmSyncLeavesUnreachableFamilyAlone pins that when a provider
// family's probe did not succeed (Running is untrustworthy), sync neither
// removes its models' routes nor restarts the proxy, and prints a warning
// naming the family. Otherwise a down ollama daemon wipes every ollama route.
func TestLitellmSyncLeavesUnreachableFamilyAlone(t *testing.T) {
	h := testHost()
	// The row carries an api_base so the write has nothing to repair (#202):
	// this test is about the family's routes being left alone.
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, api_base: \"http://localhost:11434\", additional_drop_params: [reasoning_effort]}\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n"
	p := litellmEnv(t, body)
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: false},
		},
	})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, litellmTestConfig(), true, false); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
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
	h := testHost()
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, additional_drop_params: [reasoning_effort]}\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	p := litellmEnv(t, body)
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: false},
		},
	})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil {
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
	h := testHost()
	body := "model_list:\n  - model_name: llamacpp/m\n    litellm_params: {model: openai/local-model}\n"
	p := litellmEnv(t, body)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "llamacpp", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8080"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "llamacpp/m", ProviderID: "llamacpp", ModelName: "m", Location: config.LocationLocal})
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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

// TestLitellmSyncWorksOnValidationOnlyError pins that sync gates only on a
// config LOAD failure. A registry gap in an unrelated model (unknown
// provider: the registry loads, validation fails) must not block the sync:
// one bad row would otherwise leave every other model's route stale until
// the registry was fixed. The running healthy model is still routed, and the
// broken model gets no route. The registry is the shared sample plus a model
// naming provider "ghost", which has no row (`wt-model model init` seeds a
// row only for a provider it has a default for; any other is a hand edit).
func TestLitellmSyncWorksOnValidationOnlyError(t *testing.T) {
	h := testHost()
	sample, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml"))
	if err != nil {
		t.Fatal(err)
	}
	modelHome(t, string(sample)+"\n[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n")
	// The sample has a claude-subscription model, so the sync asks the
	// proxy's environment (litellm.LoadProxyEnv). What keeps the real plist
	// unread is litellmEnv's WT_LITELLM_RESTART_CMD: with a restart command
	// configured LoadProxyEnv never opens the plist (and the check then fails
	// closed on the unknown environment). The scratch WT_LITELLM_PLIST is
	// only a backstop should that ever change.
	t.Setenv("WT_LITELLM_PLIST", filepath.Join(t.TempDir(), "proxy.plist"))
	p := litellmEnv(t, "model_list: []\n")
	if cfg, err := config.Load(); err != nil || cfg.Validate() == nil {
		t.Fatalf("fixture: loadErr=%v, want a registry that loads and fails validation", err)
	}
	// sync routes the fixture's cloud models too; an unset secret_ref is a
	// per-id build error (exit 1), which is not what this test is about.
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("PINNED_CLOUD_API_KEY", "sk-test")
	const good = "ollama/contract-fixture:local"
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: good, ModelName: "contract-fixture:local", Registered: true, Running: true},
		}})
	if _, _, err := execWTModel(t, h, "litellm", "sync"); err != nil {
		t.Fatalf("sync refused: %v", err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), good) || strings.Contains(string(b), "ghost/m") {
		t.Fatalf("sync did not route the running model, or wrote a ghost route:\n%s", b)
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

// TestLitellmSyncDryRunJSON pins `wt-model litellm sync --dry-run --json`: the plan
// is printed as one JSON document (dry_run true, the plan's id lists) and
// config.yaml is not written.
func TestLitellmSyncDryRunJSON(t *testing.T) {
	h := testHost()
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	before, _ := os.ReadFile(p)
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, true); err != nil {
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

// TestLitellmSyncDryRunJSONMatchesContract pins the `sync --dry-run --json`
// document against the fixture's sync_dry_run block
// (docs/contracts/litellm-cli.sample.json). No program in this repository
// reads that block — nothing here calls `wt-model litellm sync --dry-run --json` —
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
		Aliases: []litellm.AliasPlan{{
			ID: "alias/x", Action: litellm.AliasActionRoute,
			Live:    []litellm.AliasLive{{Target: "openrouter/x/y", Order: 1}},
			Skipped: []litellm.AliasSkip{{Target: "ollama/llama3.2:3b", Reason: "not running"}},
		}},
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

// TestLitellmSyncJSONMatchesContract pins `wt-model litellm sync --json` against the
// shared fixture's sync block. The result comes from a real litellm.Sync over
// a config.yaml that exercises every outcome — unrouted, rewritten, adopted,
// routed (a cloud model, and since #179 Phase B a discovered local model under
// its discovered id, written marked) and a per-id error — so the action names
// are pinned where Sync assigns them, not restated by hand. The injected restart hook fails with
// restart.go's warning text, so the fixture also carries a non-empty
// warnings array. The fixture (docs/contracts/litellm-cli.sample.json) is
// read by this test: a script that parses `wt-model litellm sync --json` breaks if
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
	h := testHost()
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n"
	p := litellmEnv(t, body)
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusUnreachable},
		Down:      map[string]bool{"ollama": true},
	}
	stubProbeInventory(h, snap)
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), false, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, false); err != nil {
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

// TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant pins that a stopped
// provider (refused probe) warns only when the refusal affects a route: a
// local route of its family in config.yaml (treated as stale), or a registry
// cloud model it serves (routed, but failing until it is back). A stopped
// provider with neither — the everyday state of an unused mtplx — is silent.
func TestLitellmSyncRefusedProviderWarnsOnlyWhenRelevant(t *testing.T) {
	h := testHost()
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
			stubProbeInventory(h, snap)
			var out, errOut bytes.Buffer
			if err := h.runLitellmSync(&out, &errOut, c.cfg(), true, true); err != nil {
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

// TestLitellmSyncRoutesDiscoveredModels drives the real `wt-model litellm sync`
// over a stubbed probe (#179 Phase B): a pulled ollama model with no registry
// overlay gets a marked route under its discovered id, a running mtplx model
// with a two-slash discovered id gets one too, and a family whose probe did
// not succeed (omlx, unreachable) keeps its existing discovered route instead
// of losing it to a probe that could not see it.
func TestLitellmSyncRoutesDiscoveredModels(t *testing.T) {
	h := testHost()
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
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/llama3.2:3b", ModelName: "llama3.2:3b", Artifact: "llama3.2:3b", ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/org/Qwen3.8-27B", ModelName: "org/Qwen3.8-27B", Artifact: "org/Qwen3.8-27B", ArtifactKnown: true, Running: true},
		},
	})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, snap)
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	if got := routeRows(t, p); len(got) != 0 {
		t.Fatalf("rows = %v, want the ollama/foo route gone", got)
	}
}

// TestStartHookAndSyncAgreeOnDiscoveredRoute pins, end to end, that the
// start hook and `wt-model litellm sync` name a discovered model's route the same
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
	h := testHost()
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

	stubProbeInventory(h, snap)
	var out, errOut bytes.Buffer
	// The dry run first: it has no under-lock Recheck, so it shows the plan
	// built from desiredLocalModels alone.
	if err := h.runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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
	h := testHost()
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
  store_model_in_db: true
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
			stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
			var out, errOut bytes.Buffer
			if err := h.runLitellmSync(&out, &errOut, tc.cfg, true, true); err != nil {
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
			if err := h.runLitellmSync(&out, &errOut, tc.cfg, true, false); err != nil {
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
	h := testHost()
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
			stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
			for _, dryRun := range []bool{true, false} {
				var out, errOut bytes.Buffer
				if err := h.runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := h.runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable},
	})
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := h.runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/llama3.2:3b", ModelName: "llama3.2:3b", Artifact: "llama3.2:3b", ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/q8", ModelName: "q8", Artifact: "q8", ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/new-model", ModelName: "new-model", Artifact: "new-model", ArtifactKnown: true, Running: true},
		},
	})
	var out, errOut bytes.Buffer
	if err := h.runLitellmSync(&out, &errOut, cfg, true, true); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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
	h := testHost()
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
	stubProbeInventory(h, snap)
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
		if err := h.runLitellmSync(&out, &errOut, cfg, true, false); err != nil {
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

// TestLitellmSyncWarnsAboutAnOllamaServeRow pins #206 at the command: `wt-model
// litellm sync` and its dry run both name a hand-written row LiteLLM starts
// its own `ollama serve` for and wt cannot repair (the word ollama in a model
// that is not an ollama/ one, no api_base), in JSON `warnings` and as a
// stderr `warning:` line. The dry run must say it too, or it promises a clean
// run the real sync does not deliver; neither changes the row or fails.
func TestLitellmSyncWarnsAboutAnOllamaServeRow(t *testing.T) {
	h := testHost()
	body := "model_list:\n  - model_name: hand/proxy\n    litellm_params:\n      model: openai/ollama-proxy\n"
	p := litellmEnv(t, body)
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	const want = `row "hand/proxy" (model openai/ollama-proxy) has no api_base: LiteLLM starts its own "ollama serve" for it at proxy startup; give the row an api_base`
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, dryRun); err != nil {
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
		if err := h.runLitellmSync(&out, &errOut, litellmCloudTestConfig(), false, dryRun); err != nil {
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
	h := testHost()
	cfg := litellmTestConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "corp/a", ProviderID: "corp", ModelName: "a", Location: config.LocationCloud},
		config.Model{ID: "corp/b", ProviderID: "corp", ModelName: "b"},
	)
	want := `provider "corp" has no [[providers]] row in ` + config.RegistryPath() + ", so wt cannot route corp/a, corp/b; add the row (`wt-model model init` adds the default ones) or fix the provider_id"
	for _, dryRun := range []bool{true, false} {
		litellmEnv(t, "model_list: []\n")
		stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
		var out, errOut bytes.Buffer
		if err := h.runLitellmSync(&out, &errOut, cfg, false, dryRun); err != nil {
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
	if err := h.runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil || errOut.Len() != 0 {
		t.Errorf("no gap: err = %v, stderr = %q; want silence", err, errOut.String())
	}
}
