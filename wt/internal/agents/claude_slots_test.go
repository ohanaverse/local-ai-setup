package agents

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestClaudeSlotNamesAndReservedKeys pins the slots claude declares, in
// display order, and the eleven env keys it reserves. If the order regresses
// the picker's slot line and `wt profile show` list slots in the wrong order;
// if a reserved key goes missing a profile can set it and static validation
// no longer says so.
func TestClaudeSlotNamesAndReservedKeys(t *testing.T) {
	d := claudeDriver{}
	wantSlots := []string{"fable", "opus", "sonnet", "haiku"}
	if got := d.SlotNames(); !slices.Equal(got, wantSlots) {
		t.Fatalf("SlotNames = %v, want %v", got, wantSlots)
	}
	// A caller that sorts or edits the result must not change the driver.
	d.SlotNames()[0] = "changed"
	if got := d.SlotNames(); !slices.Equal(got, wantSlots) {
		t.Fatalf("SlotNames after a caller edited its copy = %v, want %v", got, wantSlots)
	}
	wantKeys := []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
	}
	if got := d.ReservedEnvKeys(); !slices.Equal(got, wantKeys) {
		t.Fatalf("ReservedEnvKeys = %v, want %v", got, wantKeys)
	}
	r, ok := Driver(d).(SlotPassthroughRefuser)
	if !ok {
		t.Fatal("claudeDriver does not implement SlotPassthroughRefuser")
	}
	if got, want := r.RefusedSlotPassthrough(), []string{"--settings", "--model"}; !slices.Equal(got, want) {
		t.Fatalf("RefusedSlotPassthrough = %v, want %v", got, want)
	}
}

// slotTestKey is the proxy key the slot tests use. Tests search the argv for
// it to prove the key stays out of the process list.
const slotTestKey = "sk-slot-test"

// slotTestLaunch is a resolved claude profile: sonnet is the default, fable is
// unfilled and so holds sonnet's model, and haiku is a local model. The ids
// are illustrative. Every call returns fresh maps.
func slotTestLaunch() profiles.SlotLaunch {
	opus := config.Model{ID: "openrouter/z-ai/glm-5.3-flash", ProviderID: "openrouter", ModelName: "z-ai/glm-5.3-flash", Location: config.LocationCloud}
	sonnet := config.Model{ID: "ollama/glm-5.3-flash:cloud", ProviderID: "ollama", ModelName: "glm-5.3-flash:cloud", Location: config.LocationCloud}
	haiku := config.Model{ID: "omlx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "omlx", ModelName: "mlx-community--Qwen3.8-27B-4bit", Location: config.LocationLocal}
	return profiles.SlotLaunch{
		Agent:   "claude",
		Default: "sonnet",
		Models:  map[string]config.Model{"fable": sonnet, "opus": opus, "sonnet": sonnet, "haiku": haiku},
		Env: map[string]string{
			"MAX_THINKING_TOKENS": "4096",
			"SHARED":              "from-env",
		},
		ConfigContent: map[string]any{
			"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"},
			"env": map[string]any{
				"SHARED":                     "from-config-content",
				"CLAUDE_CODE_SUBAGENT_MODEL": "ollama/glm-5.3-flash:cloud",
			},
		},
	}
}

// slotTestInput is slotTestLaunch with the proxy origin and key a launch adds.
func slotTestInput(yolo bool) profiles.SlotBuildInput {
	return profiles.SlotBuildInput{
		SlotLaunch:  slotTestLaunch(),
		ProxyOrigin: "http://localhost:4000",
		ProxyKey:    slotTestKey,
		Yolo:        yolo,
	}
}

// slotSettings decodes the --settings value of a built command.
func slotSettings(t *testing.T, args []string) map[string]any {
	t.Helper()
	raw, ok := argsFlagValue(args, "--settings")
	if !ok {
		t.Fatalf("no --settings in %v", args)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("--settings is not a JSON object: %v\n%s", err, raw)
	}
	return obj
}

// TestClaudeBuildSlotsCommand pins everything of the slot build except the
// payload: the binary, the argument order with and without yolo, the process
// env, and what is removed from the inherited env. If it regresses a profile
// session starts on the wrong slot, talks to Anthropic instead of the proxy,
// or shows the proxy key in the process list.
func TestClaudeBuildSlotsCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yolo     bool
		wantHead []string
	}{
		{"plain", false, []string{"--model", "sonnet", "--settings"}},
		{"yolo", true, []string{"--dangerously-skip-permissions", "--model", "sonnet", "--settings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := claudeDriver{}.BuildSlots(slotTestInput(tc.yolo))
			if err != nil {
				t.Fatal(err)
			}
			if sc.Bin != "claude" {
				t.Errorf("Bin = %q, want claude", sc.Bin)
			}
			if len(sc.Args) != len(tc.wantHead)+1 || !slices.Equal(sc.Args[:len(tc.wantHead)], tc.wantHead) {
				t.Fatalf("Args = %v, want %v followed by the payload", sc.Args, tc.wantHead)
			}
			wantEnv := []string{
				"ANTHROPIC_BASE_URL=http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN=" + slotTestKey,
				"ANTHROPIC_API_KEY=",
			}
			if !slices.Equal(sc.Env, wantEnv) {
				t.Errorf("Env = %v, want %v", sc.Env, wantEnv)
			}
			if want := []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"}; !slices.Equal(sc.ClearEnv, want) {
				t.Errorf("ClearEnv = %v, want %v", sc.ClearEnv, want)
			}
			if want := []string{"ANTHROPIC_DEFAULT_"}; !slices.Equal(sc.ClearEnvPrefixes, want) {
				t.Errorf("ClearEnvPrefixes = %v, want %v", sc.ClearEnvPrefixes, want)
			}
			for _, a := range sc.Args {
				if strings.Contains(a, slotTestKey) || strings.Contains(a, "ANTHROPIC_AUTH_TOKEN") {
					t.Errorf("the proxy key reached argv: %s", a)
				}
			}
		})
	}
}

// TestClaudeBuildSlotsSettingsGolden compares the whole --settings payload
// with the golden file: the unfilled fable slot holds the default's model, a
// _NAME variable beside each slot, the profile's env under config_content.env
// under wt's own variables, config_content merged in, and no token. If it
// regresses a slot sends an Anthropic id the proxy rejects, or the payload
// carries the proxy key.
func TestClaudeBuildSlotsSettingsGolden(t *testing.T) {
	sc, err := claudeDriver{}.BuildSlots(slotTestInput(false))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := argsFlagValue(sc.Args, "--settings")
	if !ok {
		t.Fatalf("no --settings in %v", sc.Args)
	}
	var got bytes.Buffer
	if err := json.Indent(&got, []byte(raw), "", "  "); err != nil {
		t.Fatalf("--settings is not JSON: %v\n%s", err, raw)
	}
	got.WriteByte('\n')
	want, err := os.ReadFile("testdata/claude_slots_settings.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("--settings payload differs from testdata/claude_slots_settings.golden.json\n--- got\n%s--- want\n%s", got.Bytes(), want)
	}
}

// TestClaudeBuildSlotsEnvPrecedence pins the two properties the golden file
// cannot show. wt's own variables win even over a reserved key that reached
// the build (static validation rejects one, so this is the second line of
// defence against a session redirected away from the proxy). And the build
// leaves the profile's maps alone: the same resolved profile is built again
// by `wt profile show` and by a retried launch.
func TestClaudeBuildSlotsEnvPrecedence(t *testing.T) {
	in := slotTestInput(false)
	in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "from-env"
	in.ConfigContent["env"].(map[string]any)["ANTHROPIC_BASE_URL"] = "http://elsewhere.invalid"
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	env, ok := slotSettings(t, sc.Args)["env"].(map[string]any)
	if !ok {
		t.Fatal("--settings has no env table")
	}
	for key, want := range map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/z-ai/glm-5.3-flash",
		"ANTHROPIC_BASE_URL":           "http://localhost:4000",
		"SHARED":                       "from-config-content",
		"MAX_THINKING_TOKENS":          "4096",
	} {
		if got := env[key]; got != want {
			t.Errorf("env[%s] = %v, want %q", key, got, want)
		}
	}

	if got := in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "from-env" {
		t.Errorf("the build changed the profile's env: %q", got)
	}
	if len(in.Env) != 3 {
		t.Errorf("the build added keys to the profile's env: %v", in.Env)
	}
	cc := in.ConfigContent["env"].(map[string]any)
	if len(cc) != 3 || cc["ANTHROPIC_BASE_URL"] != "http://elsewhere.invalid" {
		t.Errorf("the build changed config_content.env: %v", cc)
	}
}

// TestClaudeBuildSlotsErrors pins the three inputs the build refuses. Each
// would otherwise launch a session with a slot left on an Anthropic id, or
// with a settings payload Claude Code rejects.
func TestClaudeBuildSlotsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(in *profiles.SlotBuildInput)
		want string
	}{
		{"a declared slot has no model", func(in *profiles.SlotBuildInput) { delete(in.Models, "haiku") }, "slot haiku has no model"},
		{"config_content.env is not a table", func(in *profiles.SlotBuildInput) { in.ConfigContent["env"] = "X=1" }, "config_content.env must be a table"},
		{"the default is not a claude slot", func(in *profiles.SlotBuildInput) { in.Default = "turbo" }, `default slot "turbo" is not a claude slot`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := slotTestInput(false)
			tc.edit(&in)
			sc, err := claudeDriver{}.BuildSlots(in)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if sc.Bin != "" || len(sc.Args) != 0 || len(sc.Env) != 0 {
				t.Errorf("a refused build returned a command: %+v", sc)
			}
		})
	}
}

// TestClaudeCheckConfigContentIsTheBuildsRule pins that static validation
// and the slot build refuse the same config_content with the same words:
// whatever CheckConfigContent refuses, BuildSlots refuses with that message,
// and whatever it passes, the build accepts. Two copies of the rule could
// drift, and then the picker would offer a row whose launch fails, or block
// one that would have launched.
func TestClaudeCheckConfigContentIsTheBuildsRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		cc   map[string]any
		want string // "" means accepted
	}{
		{"no config_content", nil, ""},
		{"no env key", map[string]any{"modelOverrides": map[string]any{"a": "b"}}, ""},
		{"env is a table", map[string]any{"env": map[string]any{"MAX_THINKING_TOKENS": "4096"}}, ""},
		{"env is a string", map[string]any{"env": "x"}, "config_content.env must be a table"},
		{"env is an array", map[string]any{"env": []any{"A=1"}}, "config_content.env must be a table"},
		{"env is a number", map[string]any{"env": int64(1)}, "config_content.env must be a table"},
		// A nan where the --settings JSON is built holds nothing: without the
		// rule, TOML's `nan` decodes and the build's encode fails at the
		// launch, after the routes were written.
		{"env holds a nan", map[string]any{"env": map[string]any{"THRESHOLD": math.NaN()}},
			"config_content cannot be encoded as the --settings JSON: json: unsupported value: NaN"},
		{"a nan outside env", map[string]any{"threshold": math.Inf(1)},
			"config_content cannot be encoded as the --settings JSON: json: unsupported value: +Inf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkErr := claudeDriver{}.CheckConfigContent(tc.cc)
			in := slotTestInput(false)
			in.ConfigContent = tc.cc
			_, buildErr := claudeDriver{}.BuildSlots(in)
			msg := func(err error) string {
				if err == nil {
					return ""
				}
				return err.Error()
			}
			if msg(checkErr) != tc.want || msg(buildErr) != tc.want {
				t.Errorf("CheckConfigContent = %q, BuildSlots = %q, want %q from both", msg(checkErr), msg(buildErr), tc.want)
			}
		})
	}
}

// TestClaudeDefaultAliasOpusplan pins claude's one default alias: opusplan
// reads the opus and sonnet slots and rests on sonnet, and nothing else is
// an alias. Claude Code resolves opusplan through ANTHROPIC_DEFAULT_OPUS_MODEL
// in plan mode and ANTHROPIC_DEFAULT_SONNET_MODEL otherwise (checked live on
// 2026-10-14, #391), so a profile without both would send plan or execution
// requests to a built-in Anthropic id the proxy rejects.
func TestClaudeDefaultAliasOpusplan(t *testing.T) {
	a, ok := claudeDriver{}.DefaultAlias("opusplan")
	if !ok {
		t.Fatal(`DefaultAlias("opusplan") is not an alias`)
	}
	if !slices.Equal(a.Requires, []string{"opus", "sonnet"}) || a.Rest != "sonnet" || a.Doc == "" {
		t.Errorf("opusplan = %#v, want Requires [opus sonnet], Rest sonnet and a Doc", a)
	}
	for _, name := range []string{"sonnet", "opus", "Opusplan", "opusplan[1m]", "turbo", ""} {
		if _, ok := (claudeDriver{}).DefaultAlias(name); ok {
			t.Errorf("DefaultAlias(%q) is an alias; only opusplan is", name)
		}
	}
	got, ok := SlotDrivers().DefaultAlias("claude", "opusplan")
	if !ok || !reflect.DeepEqual(got, a) {
		t.Errorf("SlotDrivers().DefaultAlias(claude, opusplan) = %#v, %v; want the driver's own answer", got, ok)
	}
	if _, ok := SlotDrivers().DefaultAlias("codex", "opusplan"); ok {
		t.Error("SlotDrivers().DefaultAlias gave an alias to an agent without slots")
	}
}

// TestClaudeBuildSlotsOpusplan pins the build of a profile whose default is
// opusplan: --model opusplan, so Claude Code switches between the opus and
// sonnet slots with plan mode, and CLAUDE_CODE_DISABLE_1M_CONTEXT=1 in the
// settings env above the profile's own, since in plan mode Claude Code would
// otherwise promote the opus slot's id to [1m] and assume a 1M-token window
// the model does not have (#391). A profile with a slot default gets neither.
func TestClaudeBuildSlotsOpusplan(t *testing.T) {
	in := slotTestInput(false)
	in.Default = "opusplan"
	in.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] = "0"
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "opusplan", "--settings"}
	if len(sc.Args) != 4 || !slices.Equal(sc.Args[:3], want) {
		t.Fatalf("Args = %v, want %v followed by the payload", sc.Args, want)
	}
	env := slotSettings(t, sc.Args)["env"].(map[string]any)
	if got := env["CLAUDE_CODE_DISABLE_1M_CONTEXT"]; got != "1" {
		t.Errorf("CLAUDE_CODE_DISABLE_1M_CONTEXT = %v, want \"1\", over the profile's own value", got)
	}
	for _, slot := range []string{"OPUS", "SONNET"} {
		if env["ANTHROPIC_DEFAULT_"+slot+"_MODEL"] == nil {
			t.Errorf("ANTHROPIC_DEFAULT_%s_MODEL is missing; opusplan reads it", slot)
		}
	}

	plain, err := claudeDriver{}.BuildSlots(slotTestInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := slotSettings(t, plain.Args)["env"].(map[string]any)["CLAUDE_CODE_DISABLE_1M_CONTEXT"]; set {
		t.Error("a profile with a slot default got CLAUDE_CODE_DISABLE_1M_CONTEXT; only opusplan sets it")
	}
}
