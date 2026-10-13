package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// claudeSlotVarKeys lists the eight variables by hand, not through the
// driver's own key builder, so a change to that builder fails these tests.
var claudeSlotVarKeys = []string{
	"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
	"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
}

// TestClaudeBuildMapsEverySlotToTheSingleModel pins what a single-model launch
// on another endpoint sets beside --model: every slot's model variable is the
// reference --model carries, and every slot's name variable is the model's
// provider-side name, through LiteLLM and on a direct route alike. Unmapped,
// Claude Code sends an Anthropic id for a slot and the endpoint rejects it.
func TestClaudeBuildMapsEverySlotToTheSingleModel(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	keyed := config.Route{BaseOrigin: "http://localhost:9999", APIKey: "sk-real-secret", ModelRef: m.ModelName}
	for _, tc := range []struct {
		name    string
		route   config.Route
		wantRef string
	}{
		{"litellm", litellmRoute(m), "ollama/qwen3.8:27b-mlx"},
		{"direct, keyless", directRoute(m), "qwen3.8:27b-mlx"},
		{"direct, keyed", keyed, "qwen3.8:27b-mlx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := claudeDriver{}.Build(m, false, tc.route)
			if got, _ := argsFlagValue(lc.Args, "--model"); got != tc.wantRef {
				t.Fatalf("--model = %q, want %q", got, tc.wantRef)
			}
			for _, key := range claudeSlotVarKeys {
				want := tc.wantRef
				if strings.HasSuffix(key, "_NAME") {
					want = "qwen3.8:27b-mlx"
				}
				if got := envValues(lc.Env, key); !slices.Equal(got, []string{want}) {
					t.Errorf("%s entries = %v, want exactly %q", key, got, want)
				}
				// Cleared from the inherited environment, so wt's entry is
				// the only one the command carries.
				if !slices.Contains(lc.ClearEnv, key) {
					t.Errorf("ClearEnv = %v, want it to include %s", lc.ClearEnv, key)
				}
			}
			// The endpoint variables are still set, and still not cleared.
			assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", tc.route.BaseOrigin)
			if slices.Contains(lc.ClearEnv, "ANTHROPIC_BASE_URL") {
				t.Errorf("ClearEnv = %v, want only the slot variables", lc.ClearEnv)
			}
			if len(lc.ClearEnv) != len(claudeSlotVarKeys) {
				t.Errorf("ClearEnv = %v, want exactly the eight slot variables", lc.ClearEnv)
			}
		})
	}
}

// TestClaudeBuildNativeSetsNoSlotVars pins that the native launch is left
// alone: the subscription resolves its own tiers, so wt sets none of the
// eight variables and clears none of them either.
func TestClaudeBuildNativeSetsNoSlotVars(t *testing.T) {
	for _, name := range []string{"native", "opus"} {
		m := config.Model{ID: "claude/" + name, ModelName: name, ProviderID: "claude", Native: true}
		lc := claudeDriver{}.Build(m, false, config.Route{})
		for _, key := range claudeSlotVarKeys {
			assertEnvMissing(t, lc.Env, key)
			if slices.Contains(lc.ClearEnv, key) {
				t.Errorf("claude/%s: ClearEnv = %v, want it without %s", name, lc.ClearEnv, key)
			}
		}
	}
}

// singleModelTestCfg is a registry with one keyless anthropic-protocol
// provider, routed through LiteLLM or directly.
func singleModelTestCfg(litellm bool) *config.Config {
	cfg := &config.Config{Providers: []config.Provider{{
		ID:        "ollama",
		Protocols: []config.Protocol{config.ProtocolAnthropic},
		Auth:      config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"},
	}}}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: litellm, URL: "http://localhost:4000", APIKey: slotTestKey})
	return cfg
}

// TestBuildLaunchCmdClaudeReplacesInheritedSlotVars pins the built command's
// environment: a slot variable exported in the user's shell does not survive
// beside wt's, so the command holds exactly one entry per variable, wt's. An
// inherited variable wt does not set is left alone.
func TestBuildLaunchCmdClaudeReplacesInheritedSlotVars(t *testing.T) {
	fakeSlotClaude(t)
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "claude-haiku-from-the-shell")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL_NAME", "Shell Opus")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "stray-fast")
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL", "stray-subagent")
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}

	for _, tc := range []struct {
		name    string
		litellm bool
		wantRef string
	}{
		{"litellm", true, "ollama/qwen3.8:27b-mlx"},
		{"direct", false, "qwen3.8:27b-mlx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := BuildLaunchCmd("claude", m, t.TempDir(), false, singleModelTestCfg(tc.litellm), nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, key := range claudeSlotVarKeys {
				want := tc.wantRef
				if i%2 == 1 {
					want = "qwen3.8:27b-mlx"
				}
				if got := envValues(cmd.Env, key); !slices.Equal(got, []string{want}) {
					t.Errorf("%s entries = %v, want exactly %q", key, got, want)
				}
			}
			// Not wt's to strip on a single-model launch.
			if got := envValues(cmd.Env, "ANTHROPIC_SMALL_FAST_MODEL"); !slices.Equal(got, []string{"stray-fast"}) {
				t.Errorf("ANTHROPIC_SMALL_FAST_MODEL entries = %v, want the inherited value kept", got)
			}
			if got := envValues(cmd.Env, "CLAUDE_CODE_SUBAGENT_MODEL"); !slices.Equal(got, []string{"stray-subagent"}) {
				t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL entries = %v, want the inherited value kept", got)
			}
		})
	}
}

// TestBuildLaunchCmdClaudeNativeKeepsInheritedSlotVars pins the other side:
// the native launch neither sets nor removes a slot variable.
func TestBuildLaunchCmdClaudeNativeKeepsInheritedSlotVars(t *testing.T) {
	fakeSlotClaude(t)
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "claude-haiku-from-the-shell")
	cfg := &config.Config{Providers: []config.Provider{{ID: "claude", Auth: config.AuthConfig{Type: "native"}}}}
	m := config.Model{ID: "claude/native", ModelName: "native", ProviderID: "claude", Native: true}
	cmd, err := BuildLaunchCmd("claude", m, t.TempDir(), false, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range claudeSlotVarKeys {
		var want []string
		if key == "ANTHROPIC_DEFAULT_HAIKU_MODEL" {
			want = []string{"claude-haiku-from-the-shell"}
		}
		if got := envValues(cmd.Env, key); !slices.Equal(got, want) {
			t.Errorf("%s entries = %v, want %v", key, got, want)
		}
	}
}

// TestOtherDriversSetNoClaudeSlotVars pins that the slot variables are
// claude's alone: no other agent's launch gains them.
func TestOtherDriversSetNoClaudeSlotVars(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	for _, agent := range []string{"codex", "opencode", "pi", "copilot"} {
		d := ByName(agent)
		if d == nil {
			t.Fatalf("agent %s is not registered", agent)
		}
		lc := d.Build(m, false, litellmRoute(m))
		for _, key := range claudeSlotVarKeys {
			assertEnvMissing(t, lc.Env, key)
		}
	}
}
