package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

const (
	testGWOrigin = "http://gw.example:9000"
	testGWToken  = "gw-token-123"
	// providerOrigin and providerKey stand for what a direct or LiteLLM route
	// would have carried; none may ever appear in a gateway launch.
	providerOrigin = "https://openrouter.ai/api/v1"
	providerKey    = "sk-or-secret"
)

func testGW() Gateway { return Gateway{Origin: testGWOrigin, Token: testGWToken} }

// gwCloudModel is a catalog row whose gateway id differs from its registry id.
func gwCloudModel() config.Model {
	return config.Model{
		ID: "openrouter/qwen/qwen3.8-27b", GatewayID: "gw/qwen-27b",
		ModelName: "qwen/qwen3.8-27b", ProviderID: "openrouter",
		Location: config.LocationCloud, HostContext: 131072,
	}
}

// fakeBin puts an executable named name first on PATH.
func fakeBin(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGatewayRouteNativeIsEmpty pins that a native model gets the zero Route:
// a subscription model must never be pointed at the gateway or handed its
// token.
func TestGatewayRouteNativeIsEmpty(t *testing.T) {
	if r := GatewayRoute(testGW(), nativeModel("claude")); r != (config.Route{}) {
		t.Errorf("native route = %+v, want the zero Route", r)
	}
}

// TestGatewayRouteUsesGatewayID pins that the route, pi's block key and the
// claude slot env all carry GatewayID (falling back to ID), because the
// gateway resolves models by that id, not the registry's.
func TestGatewayRouteUsesGatewayID(t *testing.T) {
	m := gwCloudModel()
	r := GatewayRoute(testGW(), m)
	want := config.Route{BaseOrigin: testGWOrigin, APIKey: testGWToken, ModelRef: "gw/qwen-27b",
		Display: m.ModelName, ProviderID: m.ProviderID, Litellm: true}
	if r != want {
		t.Errorf("route = %+v, want %+v", r, want)
	}
	m.GatewayID = ""
	if got := GatewayRoute(testGW(), m).ModelRef; got != m.ID {
		t.Errorf("ModelRef without GatewayID = %q, want ID %q", got, m.ID)
	}

	// pi's block key
	m = gwCloudModel()
	path := piHomeModelsPath(t)
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if ms := f.Providers["litellm"].Models; len(ms) != 1 || ms[0].ID != "gw/qwen-27b" {
		t.Errorf("pi litellm models = %+v, want one keyed gw/qwen-27b", ms)
	}

	// claude slot env
	fakeBin(t, "claude")
	cmd, err := BuildSlotLaunchCmdGateway(gwSlotLaunch(m), t.TempDir(), false, testGW(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "gw/qwen-27b") || strings.Contains(strings.Join(cmd.Args, " "), "openrouter/qwen") {
		t.Errorf("slot settings do not carry the gateway id only: %v", cmd.Args)
	}
}

func gwSlotLaunch(m config.Model) profiles.SlotLaunch {
	models := map[string]config.Model{}
	for _, s := range claudeSlots {
		models[s] = m
	}
	return profiles.SlotLaunch{Agent: "claude", Models: models, Default: "sonnet"}
}

// leaks reports whether s holds anything a gateway launch must not carry.
func leaks(s string) bool {
	return strings.Contains(s, providerOrigin) || strings.Contains(s, providerKey) || strings.Contains(s, "openrouter.ai")
}

// TestGatewayLaunchCells pins, per driver, what a gateway launch carries: the
// gateway origin in the shape that agent's wire needs, the token, the gateway
// id as the model, and nothing of the provider's origin or key. A regression
// sends the agent straight to a provider, bypassing the gateway.
func TestGatewayLaunchCells(t *testing.T) {
	m := gwCloudModel()
	m.Location = config.LocationCloud
	// The registry row carries the provider's own origin and key, as a
	// direct or LiteLLM route would; a builder that copied them into a
	// launch would trip the leak check below.
	m.ModelInfo = map[string]any{"api_base": providerOrigin, "api_key": providerKey}
	piHomeModelsPath(t)
	if !leaks(providerOrigin) || !leaks(providerKey) {
		t.Fatal("leaks no longer recognises the provider origin or key")
	}
	for _, agent := range []string{"claude", "codex", "copilot", "opencode", "pi"} {
		t.Run(agent, func(t *testing.T) {
			fakeBin(t, agent)
			piHomeModelsPath(t)
			cmd, _, err := BuildLaunchCmdGateway(agent, m, t.TempDir(), false, testGW(), []config.Model{m}, nil)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Join(cmd.Args[1:], " ")
			env := strings.Join(cmd.Env, "\n")
			if leaks(args) || leaks(env) {
				t.Fatalf("provider origin/key leaked: args=%s", args)
			}
			if strings.Contains(args, m.ID) || strings.Contains(env, m.ID) {
				t.Fatalf("registry id %q reached the launch: args=%s", m.ID, args)
			}
			switch agent {
			case "claude":
				// The child sees the last entry of a repeated variable.
				for k, want := range map[string]string{"ANTHROPIC_BASE_URL": testGWOrigin, "ANTHROPIC_AUTH_TOKEN": testGWToken} {
					if got := envValues(cmd.Env, k); len(got) == 0 || got[len(got)-1] != want {
						t.Errorf("%s = %v, want last %q", k, got, want)
					}
				}
				if v, _ := argsFlagValue(cmd.Args, "--model"); v != "gw/qwen-27b" {
					t.Errorf("--model = %q", v)
				}
			case "codex":
				if !strings.Contains(args, testGWOrigin+"/v1/") || !strings.Contains(args, `wire_api="responses"`) || !strings.Contains(args, "gw/qwen-27b") {
					t.Errorf("codex args = %s", args)
				}
				assertEnv(t, cmd.Env, codexGatewayEnvKey, testGWToken)
			case "copilot":
				assertEnv(t, cmd.Env, "COPILOT_PROVIDER_BASE_URL", testGWOrigin+"/v1")
				assertEnv(t, cmd.Env, "COPILOT_PROVIDER_API_KEY", testGWToken)
				assertEnv(t, cmd.Env, "COPILOT_MODEL", "gw/qwen-27b")
			case "opencode":
				if !strings.Contains(args+env, testGWOrigin+"/v1") || !strings.Contains(args+env, testGWToken) || !strings.Contains(args+env, "gw/qwen-27b") {
					t.Errorf("opencode launch = args %s env %s", args, env)
				}
			case "pi":
				if v, _ := argsFlagValue(cmd.Args, "--model"); v != "litellm/gw/qwen-27b" {
					t.Errorf("--model = %q", v)
				}
			}
		})
	}
}

// TestNativeModelLaunchesBare pins that a native claude or copilot launch
// through the gateway builder carries no ANTHROPIC_BASE_URL and no token in
// env or args: the subscription must win, and the gateway token must never
// reach an agent that does not use the gateway.
func TestNativeModelLaunchesBare(t *testing.T) {
	for _, agent := range []string{"claude", "copilot", "pi"} {
		t.Run(agent, func(t *testing.T) {
			fakeBin(t, agent)
			piFile := piHomeModelsPath(t)
			cmd, _, err := BuildLaunchCmdGateway(agent, nativeModel(agent), t.TempDir(), false, testGW(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			all := strings.Join(cmd.Args, " ") + "\n" + strings.Join(cmd.Env, "\n")
			// The inherited env is not asserted on; only what the builder adds.
			for _, e := range cmd.Env {
				if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") && strings.Contains(e, testGWOrigin) {
					t.Errorf("gateway origin in env: %s", e)
				}
			}
			if strings.Contains(all, testGWToken) || strings.Contains(all, testGWOrigin) {
				t.Errorf("gateway credential reached a native launch: %s", all)
			}
			if _, err := os.Stat(piFile); err == nil {
				t.Errorf("a native %s launch wrote %s", agent, piFile)
			}
		})
	}
}

// TestSlotGatewayCmdCarriesGateway pins that a slot-profile launch through
// the gateway builder sets the gateway origin and token (process env, never
// argv), refuses a passthrough --model, and errors on an empty gateway.
func TestSlotGatewayCmdCarriesGateway(t *testing.T) {
	fakeBin(t, "claude")
	m := gwCloudModel()
	cmd, err := BuildSlotLaunchCmdGateway(gwSlotLaunch(m), t.TempDir(), true, Gateway{Origin: testGWOrigin + "/", Token: testGWToken}, []string{"--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_BASE_URL"); len(got) == 0 || got[len(got)-1] != testGWOrigin {
		t.Errorf("ANTHROPIC_BASE_URL = %v, want the trimmed gateway origin", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_AUTH_TOKEN"); len(got) == 0 || got[len(got)-1] != testGWToken {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %v", got)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, testGWToken) {
			t.Errorf("token in argv: %s", a)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "--verbose" {
		t.Errorf("passthrough not appended: %v", cmd.Args)
	}
	if _, err := BuildSlotLaunchCmdGateway(gwSlotLaunch(m), t.TempDir(), false, testGW(), []string{"--model=x"}); err == nil {
		t.Error("passthrough --model accepted")
	}
	for _, gw := range []Gateway{{}, {Origin: "/", Token: testGWToken}, {Origin: testGWOrigin}} {
		_, err := BuildSlotLaunchCmdGateway(gwSlotLaunch(m), t.TempDir(), false, gw, nil)
		if !errors.Is(err, ErrGatewayUnconfigured) {
			t.Errorf("gateway %+v: err = %v, want ErrGatewayUnconfigured", gw, err)
		}
		_, _, err = BuildLaunchCmdGateway("claude", m, t.TempDir(), false, gw, nil, nil)
		if !errors.Is(err, ErrGatewayUnconfigured) {
			t.Errorf("gateway %+v (single): err = %v, want ErrGatewayUnconfigured", gw, err)
		}
	}
}

// TestClaudeWindowFromHostContext pins the HostContext-first window: a cloud
// model's HostContext declares it even when the tiers file never rated the
// row; a window-less local row, or a native or window-less model among
// several, withdraws it; the user's own variable wins; a slot profile anchors
// on the default slot and is lowered by the smallest other slot.
func TestClaudeWindowFromHostContext(t *testing.T) {
	t.Setenv(claudeContextVar, "")
	cloud := config.Model{ID: "c/a", ProviderID: "c", ModelName: "a", HostContext: 200000}
	small := config.Model{ID: "c/b", ProviderID: "c", ModelName: "b", HostContext: 64000}
	local := config.Model{ID: "l/x", ProviderID: "l", ModelName: "x"}

	if got, ok := claudeWindowValue(cloud); !ok || got != "200000" {
		t.Errorf("cloud = %q,%v want 200000", got, ok)
	}
	if _, ok := claudeWindowValue(local); ok {
		t.Error("window-less local row declared a window")
	}
	if _, ok := claudeWindowValue(cloud, local); ok {
		t.Error("a window-less model among several must give none")
	}
	if _, ok := claudeWindowValue(cloud, nativeModel("claude")); ok {
		t.Error("a native model among several must give none")
	}
	if got, ok := claudeWindowValue(cloud, small); !ok || got != "64000" {
		t.Errorf("smallest = %q,%v want 64000", got, ok)
	}

	t.Setenv(claudeContextVar, "12345")
	if _, ok := claudeWindowValue(cloud); ok {
		t.Error("user's own variable must win")
	}
	t.Setenv(claudeContextVar, "")

	// slot profile: default slot (sonnet) anchors; a smaller other slot lowers
	// it; a window-less other slot is ignored; a window-less default gives none.
	models := map[string]config.Model{}
	for _, s := range claudeSlots {
		models[s] = cloud
	}
	models["haiku"] = small
	models["opus"] = local
	in := profiles.SlotBuildInput{SlotLaunch: profiles.SlotLaunch{Agent: "claude", Models: models, Default: "sonnet"}}
	if got, ok := claudeWindowSetting(map[string]any{}, in); !ok || got != "64000" {
		t.Errorf("slot window = %q,%v want 64000", got, ok)
	}
	models["sonnet"] = local
	if _, ok := claudeWindowSetting(map[string]any{}, in); ok {
		t.Error("window-less default slot must give none")
	}
}

// TestClaudeSlotContextOverridesTheDerivedWindow pins #689: a slot profile's
// explicit `context` wins over the derived minimum (and works when a slot has
// no window), yet still yields to the profile env and the user's environment.
func TestClaudeSlotContextOverridesTheDerivedWindow(t *testing.T) {
	cloud := config.Model{ID: "ollama/c", ProviderID: "ollama", Location: config.LocationCloud, HostContext: 200000}
	local := config.Model{ID: "omlx/l", ProviderID: "omlx", Location: config.LocationLocal}
	models := map[string]config.Model{}
	for _, s := range claudeSlots {
		models[s] = cloud
	}
	in := profiles.SlotBuildInput{SlotLaunch: profiles.SlotLaunch{Agent: "claude", Models: models, Default: "sonnet", Context: 90000}}
	if got, ok := claudeWindowSetting(map[string]any{}, in); !ok || got != "90000" {
		t.Errorf("explicit context = %q,%v want 90000", got, ok)
	}
	models["sonnet"] = local // no derived window at all: the override still declares
	if got, ok := claudeWindowSetting(map[string]any{}, in); !ok || got != "90000" {
		t.Errorf("explicit context with a window-less default = %q,%v want 90000", got, ok)
	}
	if _, ok := claudeWindowSetting(map[string]any{claudeContextVar: "1"}, in); ok {
		t.Error("the profile env must win over context")
	}
	t.Setenv(claudeContextVar, "12345")
	if _, ok := claudeWindowSetting(map[string]any{}, in); ok {
		t.Error("the user's environment must win over context")
	}
}

// gwAliasModel is an alias row as a launch sees it: its id is the gateway id
// (alias/x), its provider "alias".
func gwAliasModel() config.Model {
	return config.Model{ID: "alias/x", GatewayID: "alias/x", ProviderID: "alias", Family: "alias",
		ModelName: "x", Location: config.LocationCloud, HostContext: 131072}
}

// TestPiLaunchesAnAliasID pins pi on an alias: --model litellm/alias/x, and
// models.json's litellm block holds an entry keyed alias/x (pi splits the
// --model value on its first slash, so the alias id stays whole under the
// litellm provider and reaches the gateway verbatim).
func TestPiLaunchesAnAliasID(t *testing.T) {
	path := piHomeModelsPath(t)
	fakeBin(t, "pi")
	m := gwAliasModel()
	cmd, _, err := BuildLaunchCmdGateway("pi", m, t.TempDir(), false, testGW(), []config.Model{gwCloudModel(), m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := argsFlagValue(cmd.Args, "--model"); v != "litellm/alias/x" {
		t.Errorf("--model = %q, want litellm/alias/x", v)
	}
	f := readPiFile(t, path)
	var ids []string
	for _, e := range f.Providers["litellm"].Models {
		ids = append(ids, e.ID)
	}
	if !slices.Contains(ids, "alias/x") {
		t.Errorf("litellm models = %v, want an entry keyed alias/x", ids)
	}
	if _, ok := f.Providers["alias"]; ok {
		t.Error("pi models.json gained an \"alias\" provider block")
	}
}

// TestCodexLaunchesAnAliasID pins codex on an alias: --model alias/x through
// the gateway provider, with the gateway token, so the gateway resolves the
// alias's database rows.
func TestCodexLaunchesAnAliasID(t *testing.T) {
	fakeBin(t, "codex")
	cmd, _, err := BuildLaunchCmdGateway("codex", gwAliasModel(), t.TempDir(), false, testGW(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := argsFlagValue(cmd.Args, "--model"); v != "alias/x" {
		t.Errorf("--model = %q, want alias/x", v)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), testGWOrigin+"/v1/") {
		t.Errorf("codex args = %v, want the gateway origin", cmd.Args)
	}
	assertEnv(t, cmd.Env, codexGatewayEnvKey, testGWToken)
}
