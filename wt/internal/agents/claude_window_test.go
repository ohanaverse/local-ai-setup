package agents

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// windowIndex is a tiers index with the given context windows by
// "provider/model_name"; a window of 0 is an entry that has none, as a local
// build has.
func windowIndex(t *testing.T, windows map[string]int) tiers.Index {
	t.Helper()
	doc := ""
	for _, id := range slices.Sorted(maps.Keys(windows)) {
		provider, name, _ := strings.Cut(id, "/")
		doc += "[[models]]\nprovider_id = \"" + provider + "\"\nmodel_name = \"" + name + "\"\n"
		if w := windows[id]; w > 0 {
			doc += "context = " + strconv.Itoa(w) + "\n"
		}
	}
	ix, err := tiers.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// stubTiers makes the launch read ix as tiers.toml, with the user's own
// variable unset unless a test sets it.
func stubTiers(t *testing.T, ix tiers.Index) {
	t.Helper()
	old := loadTiers
	loadTiers = func() tiers.Index { return ix }
	t.Cleanup(func() { loadTiers = old })
	t.Setenv(claudeContextVar, "")
}

// TestClaudeBuildDeclaresTheTiersWindow pins #405 for a single-model launch:
// a gateway-routed model whose tiers.toml entry has a context gets
// CLAUDE_CODE_MAX_CONTEXT_TOKENS, so Claude Code stops assuming 200K and
// printing its "isn't recognized" notice. A model with no entry, an entry
// with no context (a local build) and a native model get nothing: wt has no
// number to give, and the notice is Claude's own message.
func TestClaudeBuildDeclaresTheTiersWindow(t *testing.T) {
	stubTiers(t, windowIndex(t, map[string]int{
		"ollama/glm-5.3:cloud":   1048576,
		"ollama/qwen3.8:27b-mlx": 0,
	}))
	for _, tc := range []struct {
		name string
		m    config.Model
		want []string
	}{
		{"cloud with a window", config.Model{ID: "ollama/glm-5.3:cloud", ProviderID: "ollama", ModelName: "glm-5.3:cloud"}, []string{"1048576"}},
		{"local build, no window", config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}, nil},
		{"not in the file", config.Model{ID: "ollama/other", ProviderID: "ollama", ModelName: "other"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := claudeDriver{}.Build(tc.m, false, litellmRoute(tc.m))
			if got := envValues(lc.Env, claudeContextVar); !slices.Equal(got, tc.want) {
				t.Errorf("%s entries = %v, want %v", claudeContextVar, got, tc.want)
			}
			// wt clears nothing for it: a user's own export is theirs.
			if slices.Contains(lc.ClearEnv, claudeContextVar) {
				t.Errorf("ClearEnv = %v, want it to leave %s alone", lc.ClearEnv, claudeContextVar)
			}
		})
	}
	native := config.Model{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Native: true}
	if got := envValues(claudeDriver{}.Build(native, false, config.Route{}).Env, claudeContextVar); got != nil {
		t.Errorf("native model got %v, want nothing", got)
	}
}

// TestClaudeWindowYieldsToTheUsersOwnValue verifies a variable already set in
// the environment wt started from is left alone, on both launch paths: it is
// one value per session and the user chose theirs. wt adding its own would
// override a deliberate smaller window.
func TestClaudeWindowYieldsToTheUsersOwnValue(t *testing.T) {
	stubTiers(t, windowIndex(t, map[string]int{"ollama/glm-5.3:cloud": 1048576}))
	t.Setenv(claudeContextVar, "123456")
	m := config.Model{ID: "ollama/glm-5.3:cloud", ProviderID: "ollama", ModelName: "glm-5.3:cloud"}
	if got := envValues(claudeDriver{}.Build(m, false, litellmRoute(m)).Env, claudeContextVar); got != nil {
		t.Errorf("single-model launch set %v over the user's value", got)
	}
	in := slotTestInput(false)
	in.Models["sonnet"] = m
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := slotSettings(t, sc.Args)["env"].(map[string]any)[claudeContextVar]; set {
		t.Error("slot profile set the variable over the user's value")
	}
}

// TestClaudeBuildSlotsDeclaresTheDefaultSlotsWindow pins the slot-profile
// rule: the variable is one value per session, so it carries the default
// slot's window and the other slots' are ignored; a profile's own env or
// config_content.env value for it wins; and a default slot with no window
// declares nothing even when another slot has one.
func TestClaudeBuildSlotsDeclaresTheDefaultSlotsWindow(t *testing.T) {
	stubTiers(t, windowIndex(t, map[string]int{
		"openrouter/big":   1000000,
		"openrouter/small": 200000,
		"ollama/local":     0,
	}))
	model := func(id string) config.Model {
		p, n, _ := strings.Cut(id, "/")
		return config.Model{ID: id, ProviderID: p, ModelName: n}
	}
	build := func(t *testing.T, mutate func(*profiles.SlotBuildInput)) map[string]any {
		t.Helper()
		in := slotTestInput(false)
		in.Models["sonnet"] = model("openrouter/big")
		in.Models["opus"] = model("openrouter/small")
		in.Models["haiku"] = model("ollama/local")
		if mutate != nil {
			mutate(&in)
		}
		sc, err := claudeDriver{}.BuildSlots(in)
		if err != nil {
			t.Fatal(err)
		}
		return slotSettings(t, sc.Args)["env"].(map[string]any)
	}

	if got := build(t, nil)[claudeContextVar]; got != "1000000" {
		t.Errorf("default sonnet: %s = %v, want the sonnet slot's 1000000, not opus's", claudeContextVar, got)
	}
	if _, set := build(t, func(in *profiles.SlotBuildInput) { in.Default = "haiku" })[claudeContextVar]; set {
		t.Error("default haiku (a local build, no window) declared a window from another slot")
	}
	if got := build(t, func(in *profiles.SlotBuildInput) { in.Env[claudeContextVar] = "4242" })[claudeContextVar]; got != "4242" {
		t.Errorf("profile env value lost: %v", got)
	}
	got := build(t, func(in *profiles.SlotBuildInput) {
		in.ConfigContent["env"].(map[string]any)[claudeContextVar] = "5151"
	})[claudeContextVar]
	if got != "5151" {
		t.Errorf("config_content.env value lost: %v", got)
	}
}

// TestClaudeBuildSlotsOpusplanTakesTheSmallerWindow verifies opusplan, which
// is the opus slot's model in plan mode and the sonnet slot's otherwise,
// declares the smaller of the two windows, and nothing when either has none:
// a window known for one says nothing about the other, which may be smaller.
func TestClaudeBuildSlotsOpusplanTakesTheSmallerWindow(t *testing.T) {
	stubTiers(t, windowIndex(t, map[string]int{
		"openrouter/big":   1000000,
		"openrouter/small": 200000,
		"ollama/local":     0,
	}))
	run := func(opus, sonnet string) (string, bool) {
		in := slotTestInput(false)
		in.Default = claudeOpusplan
		p, n, _ := strings.Cut(opus, "/")
		in.Models["opus"] = config.Model{ID: opus, ProviderID: p, ModelName: n}
		p, n, _ = strings.Cut(sonnet, "/")
		in.Models["sonnet"] = config.Model{ID: sonnet, ProviderID: p, ModelName: n}
		sc, err := claudeDriver{}.BuildSlots(in)
		if err != nil {
			t.Fatal(err)
		}
		v, ok := slotSettings(t, sc.Args)["env"].(map[string]any)[claudeContextVar].(string)
		return v, ok
	}
	if v, ok := run("openrouter/big", "openrouter/small"); !ok || v != "200000" {
		t.Errorf("opus big, sonnet small: %q, %v; want 200000", v, ok)
	}
	if v, ok := run("openrouter/small", "openrouter/big"); !ok || v != "200000" {
		t.Errorf("opus small, sonnet big: %q, %v; want 200000", v, ok)
	}
	if v, ok := run("openrouter/big", "ollama/local"); ok {
		t.Errorf("one slot with no window declared %q, want nothing", v)
	}
}
