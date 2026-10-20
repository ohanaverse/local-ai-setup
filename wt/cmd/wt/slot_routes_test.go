package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// claudeSlotVarKeys are the four variables of claude's --settings payload
// that name a slot's model, in claude's slot order.
var claudeSlotVarKeys = []string{
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

// slotSettingsOf decodes the --settings JSON in a claude slot command's args.
func slotSettingsOf(t *testing.T, args []string) map[string]any {
	t.Helper()
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args = %q, want a --settings value", args)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(args[i+1]), &obj); err != nil {
		t.Fatalf("--settings is not JSON: %v", err)
	}
	return obj
}

// slotVarsOf maps each of the four slot variables of a decoded --settings
// payload to its value. A missing or non-string variable fails the test.
func slotVarsOf(t *testing.T, settings map[string]any) map[string]string {
	t.Helper()
	env, _ := settings["env"].(map[string]any)
	out := map[string]string{}
	for _, k := range claudeSlotVarKeys {
		v, ok := env[k].(string)
		if !ok || v == "" {
			t.Fatalf("payload env %s = %#v, want a model id", k, env[k])
		}
		out[k] = v
	}
	return out
}

// slotRoutesFixtureConfig is slotFixtureConfig with two more cloud models,
// "openrouter/c" and "openrouter/d", so a profile can fill all four slots
// with distinct models.
func slotRoutesFixtureConfig() *config.Config {
	cfg := slotFixtureConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/c", ProviderID: "openrouter", ModelName: "c", Family: "c", Tags: []string{"code"}},
		config.Model{ID: "openrouter/d", ProviderID: "openrouter", ModelName: "d", Family: "d", Tags: []string{"code"}},
	)
	return cfg
}

// slotChecksFromTOML runs the real parse and static validation of a
// slot-profiles.toml body, as the app does at launch, and fails the test on
// a parse error or a profile that does not pass.
func slotChecksFromTOML(t *testing.T, body string) []profiles.SlotCheck {
	t.Helper()
	store, err := profiles.ParseSlots([]byte(body))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	checks := profiles.ValidateSlots(store, agents.SlotDrivers())
	for _, c := range checks {
		if !c.Usable || c.Err != nil {
			t.Fatalf("fixture: profile %s is not valid: %v", c.Profile.Name, c.Err)
		}
	}
	return checks
}

// TestSlotProfileLaunchRoutesEverySlotModel runs a -M profile/<name> launch
// through the real route writes into a scratch config.yaml and the real
// claude slot build, and checks that every model id the --settings payload
// hands Claude Code (the four ANTHROPIC_DEFAULT_<SLOT>_MODEL values, and each
// modelOverrides target and CLAUDE_CODE_SUBAGENT_MODEL the profile sets in
// config_content) is a model_name in that config.yaml. It covers a profile
// that fills all four slots, one that fills only sonnet and haiku (the
// unfilled slots hold the default's model), an opusplan default, and one
// with a local model, which is routed because the inventory reports it
// running. A slot value the proxy has no row for fails at request time with
// "Invalid model name", mid-session, on whichever tier Claude Code happened
// to use (a /model switch, a subagent, a background haiku call): the #380
// live test could only spot-check this.
//
// It lives in cmd/wt beside slot_launch_test.go because that is where
// launchSlotProfile and its seams are: it runs the whole non-TUI launch with
// only the proxy restart, the probe and the agent binary stood in for, so a
// change to which models step 3 or 5 hands the route write is caught too.
func TestSlotProfileLaunchRoutesEverySlotModel(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		running bool              // the inventory reports omlx/q running
		want    map[string]string // slot variable -> model id
		extra   []string          // the sorted ids config_content names (modelOverrides targets, CLAUDE_CODE_SUBAGENT_MODEL)
	}{
		{
			name: "all four slots, with modelOverrides and a subagent model",
			toml: `
[[profile]]
name = "p"
agent = "claude"
default = "opus"
[profile.slots]
fable = "openrouter/a"
opus = "openrouter/b"
sonnet = "openrouter/c"
haiku = "openrouter/d"
[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "openrouter/c"
[profile.config_content.env]
CLAUDE_CODE_SUBAGENT_MODEL = "openrouter/d"
`,
			want: map[string]string{
				"ANTHROPIC_DEFAULT_FABLE_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/b",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/c", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "openrouter/d",
			},
			extra: []string{"openrouter/c", "openrouter/d"},
		},
		{
			name: "sonnet and haiku only",
			toml: `
[[profile]]
name = "p"
agent = "claude"
default = "sonnet"
[profile.slots]
sonnet = "openrouter/a"
haiku = "openrouter/b"
`,
			want: map[string]string{
				"ANTHROPIC_DEFAULT_FABLE_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/a",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "openrouter/b",
			},
		},
		{
			name: "opusplan default",
			toml: `
[[profile]]
name = "p"
agent = "claude"
default = "opusplan"
[profile.slots]
opus = "openrouter/b"
sonnet = "openrouter/a"
`,
			want: map[string]string{
				"ANTHROPIC_DEFAULT_FABLE_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/b",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "openrouter/a",
			},
		},
		{
			name: "one running local model",
			toml: `
[[profile]]
name = "p"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
haiku = "omlx/q"
[profile.config_content.modelOverrides]
"claude-haiku-4-5" = "omlx/q"
`,
			running: true,
			want: map[string]string{
				"ANTHROPIC_DEFAULT_FABLE_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/a",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/a", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/q",
			},
			extra: []string{"omlx/q"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, worktree := slotLaunchEnv(t)
			fakeAgentOnPath(t, "claude")
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(cfgPath, []byte("model_list: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_LITELLM_CONFIG", cfgPath)
			t.Setenv("WT_LITELLM_RESTART_CMD", "false")
			if tc.running {
				stubProbeInventory(t, runningQ())
			}
			seams := stubSlotSeams(t)
			// The real route write, and the real slot build: only the
			// restart, the probe and the agent binary stay stood in for.
			ensureSlotRoutes = func(ctx context.Context, _ io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (bool, error) {
				return lifecycle.EnsureModelRoutes(ctx, io.Discard, cfg, models, owedIn)
			}
			var built *exec.Cmd
			buildSlotCmd = func(sl profiles.SlotLaunch, wt string, yolo bool, cfg *config.Config, extra []string) (*exec.Cmd, error) {
				cmd, err := agents.BuildSlotLaunchCmd(sl, wt, yolo, cfg, extra)
				built = cmd
				return cmd, err
			}
			t.Cleanup(lifecycle.WaitPendingRoutes)
			stubSlotChecks(t, slotChecksFromTOML(t, tc.toml)...)
			cfg := slotRoutesFixtureConfig()

			// A local model is not routed until something routes it: the
			// write below is the launch's, made because the model runs.
			before, err := litellm.Open(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if ids := before.RoutedIDs(); len(ids) != 0 {
				t.Fatalf("fixture: config.yaml starts with routes %v", ids)
			}

			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, cfg, "profile/p")
			})
			if err != nil {
				t.Fatalf("launch error = %v (steps %s)", err, seams.log())
			}
			if built == nil {
				t.Fatal("the slot command was never built")
			}

			f, err := litellm.Open(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			routed := f.RoutedIDs()
			settings := slotSettingsOf(t, built.Args)
			vars := slotVarsOf(t, settings)
			for k, want := range tc.want {
				if vars[k] != want {
					t.Errorf("payload %s = %q, want %q", k, vars[k], want)
				}
			}
			named := []string{}
			for _, k := range claudeSlotVarKeys {
				named = append(named, vars[k])
			}
			var fromContent []string
			env, _ := settings["env"].(map[string]any)
			if v, ok := env["CLAUDE_CODE_SUBAGENT_MODEL"].(string); ok {
				fromContent = append(fromContent, v)
			}
			if mo, ok := settings["modelOverrides"].(map[string]any); ok {
				for _, v := range mo {
					if s, ok := v.(string); ok {
						fromContent = append(fromContent, s)
					}
				}
			}
			slices.Sort(fromContent)
			if !slices.Equal(fromContent, tc.extra) {
				t.Errorf("config_content ids in the payload = %v, want %v", fromContent, tc.extra)
			}
			named = append(named, fromContent...)
			for _, id := range named {
				if !slices.Contains(routed, id) {
					t.Errorf("payload names %s, but config.yaml routes only %v", id, routed)
				}
			}
			if strings.Contains(seams.log(), "start:") {
				t.Errorf("steps = %s: no model should have been started", seams.log())
			}
		})
	}
}
