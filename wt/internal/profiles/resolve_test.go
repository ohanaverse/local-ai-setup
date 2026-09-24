// wt/internal/profiles/resolve_test.go
package profiles

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
	}
}

// TestResolveNoMatchReturnsEmpty verifies an agent/model with no matching
// profile resolves to an empty ResolvedProfile, so callers can skip the
// confirm prompt and every application step entirely on the common
// (no profile authored) path.
func TestResolveNoMatchReturnsEmpty(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	m := config.Model{ID: "claude/opus", ProviderID: "claude", ModelName: "opus"}
	rp := Resolve(store, "claude", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (cloud model, local-only profile)", rp)
	}
}

// TestResolveLocationTierMatches verifies a location-tier profile applies
// to a local model for the matching agent, and substitutes {{model_name}}
// in Env values — the mechanism the claude Phase-1 profile depends on.
func TestResolveLocationTierMatches(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "{{model_name}}",
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if rp.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "qwen3.8:27b-mlx" {
		t.Errorf("Env = %v, want substituted model name", rp.Env)
	}
}

// TestResolveModelTierOverridesLocationTierSameKey verifies the
// most-specific-wins merge rule for a colliding Env key: a model-tier
// profile's value for a key must win over a location-tier profile's value
// for the same key, while a location-tier key the model tier never
// touches survives untouched.
func TestResolveModelTierOverridesLocationTierSameKey(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{
			"MAX_THINKING_TOKENS": "4096", "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
		}},
		{Agent: "claude", Match: "model", Model: "ollama/qwen3.8:27b-mlx", Env: map[string]string{
			"MAX_THINKING_TOKENS": "8192",
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if rp.Env["MAX_THINKING_TOKENS"] != "8192" {
		t.Errorf("MAX_THINKING_TOKENS = %q, want model-tier value 8192", rp.Env["MAX_THINKING_TOKENS"])
	}
	if rp.Env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("CLAUDE_CODE_ATTRIBUTION_HEADER = %q, want untouched location-tier value 0", rp.Env["CLAUDE_CODE_ATTRIBUTION_HEADER"])
	}
}

// TestResolveConfigContentDeepMergesAcrossTiers is the regression lock for
// the code-review finding that Resolve's ConfigContent merge was a shallow
// top-level overwrite: a location-tier profile setting
// config_content.env.MAX_THINKING_TOKENS and a model-tier profile setting
// config_content.env.ANTHROPIC_DEFAULT_SONNET_MODEL both collide on the
// SAME top-level "env" key. Both nested fields must survive in the merged
// result — the model tier must not wholesale-replace the location tier's
// "env" object just because they share that one top-level key.
func TestResolveConfigContentDeepMergesAcrossTiers(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", ConfigContent: map[string]any{
			"env": map[string]any{"MAX_THINKING_TOKENS": "4096"},
		}},
		{Agent: "claude", Match: "model", Model: "ollama/qwen3.8:27b-mlx", ConfigContent: map[string]any{
			"env": map[string]any{"ANTHROPIC_DEFAULT_SONNET_MODEL": "{{model_name}}"},
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	env, ok := rp.ConfigContent["env"].(map[string]any)
	if !ok {
		t.Fatalf("ConfigContent[env] = %#v, want a map", rp.ConfigContent["env"])
	}
	if env["MAX_THINKING_TOKENS"] != "4096" {
		t.Errorf("env[MAX_THINKING_TOKENS] = %v, want 4096 (location-tier value must survive the model-tier merge)", env["MAX_THINKING_TOKENS"])
	}
	if env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "qwen3.8:27b-mlx" {
		t.Errorf("env[ANTHROPIC_DEFAULT_SONNET_MODEL] = %v, want the substituted model name", env["ANTHROPIC_DEFAULT_SONNET_MODEL"])
	}
}

// TestResolveArgsIsWholeFieldReplace verifies Args (a list, not a keyed
// map) follows whole-field replacement: a more specific tier's Args fully
// replaces a less specific tier's Args rather than concatenating, per the
// design spec's merge rule.
func TestResolveArgsIsWholeFieldReplace(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "location", Location: "local", Args: []string{"-c", "a=1"}},
		{Agent: "codex", Match: "provider", Provider: "ollama", Args: []string{"-c", "b=2"}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "codex", testCfg(), m)
	if len(rp.ExtraArgs) != 2 || rp.ExtraArgs[1] != "b=2" {
		t.Errorf("ExtraArgs = %v, want [-c b=2] (provider tier replaces location tier)", rp.ExtraArgs)
	}
}

// TestResolveProviderTierUsesModelProviderIDFallback verifies provider-tier
// matching falls back to the segment before "/" in Model.ID when
// Model.ProviderID is empty, mirroring config.ResolveRoute's own fallback
// — a discovered model (registry gap) must still match a provider-tier
// profile.
func TestResolveProviderTierUsesModelProviderIDFallback(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "provider", Provider: "ollama", Args: []string{"-c", "x=1"}},
	}}
	m := config.Model{ID: "ollama/some-discovered-model", ModelName: "some-discovered-model"} // ProviderID intentionally empty
	rp := Resolve(store, "codex", testCfg(), m)
	if len(rp.ExtraArgs) == 0 {
		t.Errorf("ExtraArgs empty, want provider-tier match via ID fallback")
	}
}

// TestResolveDisabledAgentSkipsOtherAgentsProfiles verifies a profile for
// a different agent never leaks into another agent's resolution.
func TestResolveDisabledAgentSkipsOtherAgentsProfiles(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "pi", Match: "location", Location: "local", Wrapper: &WrapperSpec{Binary: "little-coder", ArgsTemplate: []string{"{{args}}"}}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() for claude = %+v, want Empty() (profile is for pi)", rp)
	}
}

// TestMatchesTierRejectsEmptyFieldEvenWithZeroModel is the regression lock
// for the code-review finding that matchesTier treated an empty/unset
// match-tier field as a valid match value: a malformed profile with
// `match = "model"` but no `model = ...` line (Model == "") used to match
// ANY zero-value config.Model (m.ID == ""), which `wt profile show -A
// <agent>` (no -M) resolves with — directly contradicting the design's "no
// tier can match without a model" comment. Now it must never match.
func TestMatchesTierRejectsEmptyFieldEvenWithZeroModel(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "model", Env: map[string]string{"X": "1"}}, // Model left empty (malformed)
	}}
	rp := Resolve(store, "claude", testCfg(), config.Model{})
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (an empty Model field must never match, even against a zero-value model)", rp)
	}
}

// TestMatchesTierRejectsEmptyProviderField verifies the same empty-field
// guard for the provider tier: a malformed profile with `match =
// "provider"` and no `provider = ...` line must never match a model whose
// own provider fallback also happens to be empty.
func TestMatchesTierRejectsEmptyProviderField(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "provider", Args: []string{"-c", "x=1"}}, // Provider left empty (malformed)
	}}
	m := config.Model{ID: "no-slash-in-this-id"} // providerID(m) falls back to "" too
	rp := Resolve(store, "codex", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (an empty Provider field must never match)", rp)
	}
}
