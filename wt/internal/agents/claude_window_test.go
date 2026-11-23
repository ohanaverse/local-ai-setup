package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// windowModels returns a constructor for "provider/model_name" ids whose
// models carry the HostContext the model server reports, from windows (0 or
// absent: none, as a local build has). It clears the user's own variable
// unless a test sets it. The server decides which tiers.toml window is a
// host's (#470, internal/modelserver's TestCatalogContextOnlyWithHostBasis);
// the driver reads only HostContext.
func windowModels(t *testing.T, windows map[string]int) func(id string) config.Model {
	t.Helper()
	t.Setenv(claudeContextVar, "")
	return func(id string) config.Model {
		p, n, _ := strings.Cut(id, "/")
		return config.Model{ID: id, ProviderID: p, ModelName: n, HostContext: windows[id]}
	}
}

// TestClaudeBuildDeclaresTheTiersWindow pins #405 for a single-model launch:
// a gateway-routed model whose HostContext (the host's tiers.toml window, as
// the model server reports it) is set gets
// CLAUDE_CODE_MAX_CONTEXT_TOKENS, so Claude Code stops assuming 200K and
// printing its "isn't recognized" notice. A model with no entry, an entry
// with no context (a local build) and a native model get nothing: wt has no
// number to give, and the notice is Claude's own message.
func TestClaudeBuildDeclaresTheTiersWindow(t *testing.T) {
	model := windowModels(t, map[string]int{"ollama/glm-5.3:cloud": 1048576})
	for _, tc := range []struct {
		name string
		m    config.Model
		want []string
	}{
		{"cloud with a window", model("ollama/glm-5.3:cloud"), []string{"1048576"}},
		{"local build, no window", model("ollama/qwen3.8:27b-mlx"), nil},
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
	m := windowModels(t, map[string]int{"ollama/glm-5.3:cloud": 1048576})("ollama/glm-5.3:cloud")
	t.Setenv(claudeContextVar, "123456")
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

// TestClaudeBuildSlotsDeclaresTheSmallestSlotWindow pins the slot-profile
// rule (#471): the variable is one value per session and /model can move the
// session onto any slot, so it carries the smallest window among the slots
// that have one, provided the default slot has one. A slot with no window (a
// local build) is ignored and does not veto; a default slot with none
// declares nothing even when another slot has one; and a profile's own env or
// config_content.env value for it wins.
func TestClaudeBuildSlotsDeclaresTheSmallestSlotWindow(t *testing.T) {
	model := windowModels(t, map[string]int{"openrouter/big": 1000000, "openrouter/small": 200000})
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

	if got := build(t, nil)[claudeContextVar]; got != "200000" {
		t.Errorf("default sonnet 1M, opus 200K: %s = %v, want the smallest, 200000 (a /model opus must not keep 1M)", claudeContextVar, got)
	}
	if got := build(t, func(in *profiles.SlotBuildInput) { in.Models["opus"] = model("openrouter/big") })[claudeContextVar]; got != "1000000" {
		t.Errorf("every slot with a window has 1M, one local slot beside them: %s = %v, want 1000000 (the local slot does not veto)", claudeContextVar, got)
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
	model := windowModels(t, map[string]int{"openrouter/big": 1000000, "openrouter/small": 200000})
	run := func(opus, sonnet string) (string, bool) {
		in := slotTestInput(false)
		in.Default = claudeOpusplan
		in.Models["opus"] = model(opus)
		in.Models["sonnet"] = model(sonnet)
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
	// And a slot outside the pair that has a smaller window lowers it, as for
	// any default (#471): /model can still move the session there.
	in := slotTestInput(false)
	in.Default = claudeOpusplan
	big := model("openrouter/big")
	in.Models["opus"], in.Models["sonnet"] = big, big
	in.Models["haiku"] = model("openrouter/small")
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	if v := slotSettings(t, sc.Args)["env"].(map[string]any)[claudeContextVar]; v != "200000" {
		t.Errorf("opusplan with a smaller haiku: %v, want 200000", v)
	}
}

// aliasModel is the session's model for an alias row as the model server
// sends it (#596): id and gateway id the alias id, model name its bare name,
// Context the minimum over its targets' windows (0 when one is unknown),
// converted by catalog.ToModel as every launch path converts a row.
func aliasModel(name, loc string, context int, targets ...modelapi.AliasTarget) config.Model {
	id := "alias/" + name
	return catalog.ToModel(modelapi.Row{ID: id, GatewayID: id, ProviderID: "alias", Family: "alias", ModelName: name,
		Kind: modelapi.KindAlias, Location: loc, Context: context, Registered: true,
		State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Targets: targets})
}

// TestClaudeSlotWithAliasDeclaresItsWindow pins a claude slot naming an
// alias: the slot's model variable carries the alias id (the gateway routes
// it to the alias's database rows), its _NAME the alias's bare name, and the
// declared window is the alias's (the server's minimum over its targets)
// under the unchanged slot rule; an alias whose window the server withholds
// (a target unknown) declares nothing as the default slot. Over-declaring
// would make Claude Code compact too late on the smaller target.
func TestClaudeSlotWithAliasDeclaresItsWindow(t *testing.T) {
	t.Setenv(claudeContextVar, "")
	build := func(t *testing.T, m config.Model) map[string]any {
		t.Helper()
		in := slotTestInput(false)
		in.Models["sonnet"] = m
		sc, err := claudeDriver{}.BuildSlots(in)
		if err != nil {
			t.Fatal(err)
		}
		return slotSettings(t, sc.Args)["env"].(map[string]any)
	}
	group := aliasModel("sonnet-main", "cloud", 200000,
		modelapi.AliasTarget{ID: "openrouter/big", Order: 1, ProviderID: "openrouter", Location: "cloud", Context: 1000000},
		modelapi.AliasTarget{ID: "omlx/small", Order: 2, ProviderID: "omlx", Location: "local", Context: 200000})
	env := build(t, group)
	for k, want := range map[string]string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL":      "alias/sonnet-main",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "sonnet-main",
		claudeContextVar:                      "200000",
	} {
		if got := env[k]; got != want {
			t.Errorf("%s = %v, want %q", k, got, want)
		}
	}
	unknown := aliasModel("sonnet-main", "cloud", 0,
		modelapi.AliasTarget{ID: "openrouter/big", Order: 1, ProviderID: "openrouter", Location: "cloud", Context: 1000000},
		modelapi.AliasTarget{ID: "omlx/small", Order: 2, ProviderID: "omlx", Location: "local"})
	if v, set := build(t, unknown)[claudeContextVar]; set {
		t.Errorf("an alias with an unknown target declared %v, want nothing", v)
	}
}

// TestClaudeSlotWithTwoAliasesDeclaresTheMinimum pins the slot rule over two
// aliases: sonnet (the default) a cloud alias, haiku a local-only alias whose
// target has a registry context_window (#626). /model can move the session
// onto haiku, so the declared window is the smaller, haiku's; a session that
// dropped a local row's window would keep sonnet's and overrun haiku.
func TestClaudeSlotWithTwoAliasesDeclaresTheMinimum(t *testing.T) {
	t.Setenv(claudeContextVar, "")
	in := slotTestInput(false)
	in.Models["sonnet"] = aliasModel("sonnet-main", "cloud", 400000,
		modelapi.AliasTarget{ID: "openrouter/big", Order: 1, ProviderID: "openrouter", Location: "cloud", Context: 400000})
	in.Models["haiku"] = aliasModel("haiku-local", "local", 131072,
		modelapi.AliasTarget{ID: "omlx/small", Order: 1, ProviderID: "omlx", Location: "local", Context: 131072})
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	env := slotSettings(t, sc.Args)["env"].(map[string]any)
	if got := env[claudeContextVar]; got != "131072" {
		t.Errorf("%s = %v, want 131072 (the local alias's window)", claudeContextVar, got)
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "alias/haiku-local" {
		t.Errorf("ANTHROPIC_DEFAULT_HAIKU_MODEL = %v, want alias/haiku-local", got)
	}
}
