package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelgateway"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// subTestToken is the wt model-server token of the header-mode tests (the
// plan's wt token sentinel). Every test searches the argv, the --settings
// payload and error text for it.
const subTestToken = "wt-SENTINEL-tok"

// subModel is a subscription row converted by catalog.ToModel, as every
// launch path converts one.
func subModel(id string) config.Model {
	return catalog.ToModel(modelapi.Row{ID: id, GatewayID: id, ProviderID: config.SubscriptionProvider, ModelName: id,
		Family: "claude", Location: "cloud", Context: 1000000, Registered: true,
		State: modelapi.StateRunning, Action: modelapi.ActionLaunch})
}

// subSlotInput is the mixed profile of decision 7: fable and opus on the
// subscription, sonnet and haiku on aliases, resting on opus.
func subSlotInput() profiles.SlotBuildInput {
	return profiles.SlotBuildInput{
		SlotLaunch: profiles.SlotLaunch{
			Agent:   "claude",
			Default: "opus",
			Models: map[string]config.Model{
				"fable":  subModel("claude-fable-5-2"),
				"opus":   subModel("claude-opus-5-5"),
				"sonnet": aliasModel("sonnet-main", "cloud", 200000),
				"haiku":  aliasModel("haiku-local", "cloud", 32768),
			},
		},
		ProxyOrigin:  "http://127.0.0.1:4100",
		ProxyKey:     subTestToken,
		Subscription: true,
	}
}

// settingsEnv decodes the env table of a built command's --settings value.
func settingsEnv(t *testing.T, args []string) map[string]any {
	t.Helper()
	env, ok := slotSettings(t, args)["env"].(map[string]any)
	if !ok {
		t.Fatal("--settings has no env table")
	}
	return env
}

// lastEnv is the value of the last entry for key in env, and whether one
// exists (the last entry is the one a process sees).
func lastEnv(env []string, key string) (string, bool) {
	val, found := "", false
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			val, found = v, true
		}
	}
	return val, found
}

// envCount is how many entries of env name key.
func envCount(env []string, key string) int {
	n := 0
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && k == key {
			n++
		}
	}
	return n
}

// TestClaudeBuildSlotsSubscriptionEnv pins the header-mode slot build
// (decision 7): the process env is only the gateway url and an empty
// ANTHROPIC_API_KEY, with no ANTHROPIC_AUTH_TOKEN, so Claude Code keeps its
// own login; the wt token travels only as HeaderToken, which the env
// assembler turns into ANTHROPIC_CUSTOM_HEADERS; a stray inherited
// ANTHROPIC_AUTH_TOKEN or CLAUDE_CODE_OAUTH_TOKEN is cleared so the checked
// login is the credential used; and the token is in no argument, the
// --settings payload included. The whole payload is compared with a second
// golden file. If it regresses the token lands in the process list or a
// settings payload, or a paid key or another token displaces the login.
func TestClaudeBuildSlotsSubscriptionEnv(t *testing.T) {
	sc, err := claudeDriver{}.BuildSlots(subSlotInput())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:4100", "ANTHROPIC_API_KEY="}; !slices.Equal(sc.Env, want) {
		t.Errorf("Env = %v, want %v", sc.Env, want)
	}
	if sc.HeaderToken != subTestToken {
		t.Errorf("HeaderToken is not the wt token")
	}
	for _, e := range sc.Env {
		if strings.HasPrefix(e, "ANTHROPIC_AUTH_TOKEN=") || strings.Contains(e, subTestToken) {
			t.Errorf("Env carries the token or an auth token: %q", e)
		}
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if !slices.Contains(sc.ClearEnv, k) {
			t.Errorf("ClearEnv %v lacks %s", sc.ClearEnv, k)
		}
	}
	for _, a := range sc.Args {
		if strings.Contains(a, subTestToken) {
			t.Errorf("the wt token reached argv")
		}
	}
	raw, _ := argsFlagValue(sc.Args, "--settings")
	if strings.Contains(raw, subTestToken) {
		t.Error("the wt token reached the --settings payload")
	}
	env := settingsEnv(t, sc.Args)
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("--settings env names %s", k)
		}
	}
	for k, want := range map[string]string{
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "claude-fable-5-2",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "claude-opus-5-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "alias/sonnet-main",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "alias/haiku-local",
	} {
		if env[k] != want {
			t.Errorf("settings env[%s] = %v, want %q", k, env[k], want)
		}
	}

	var got bytes.Buffer
	if err := json.Indent(&got, []byte(raw), "", "  "); err != nil {
		t.Fatalf("--settings is not JSON: %v", err)
	}
	got.WriteByte('\n')
	want, err := os.ReadFile("testdata/claude_slots_subscription_settings.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("--settings payload differs from testdata/claude_slots_subscription_settings.golden.json\n--- got\n%s--- want\n%s", got.Bytes(), want)
	}
}

// TestClaudeBuildSlotsTokenModeUnchanged pins that a profile with no
// subscription model gets no HeaderToken: the token-mode build stays what
// TestClaudeBuildSlotsCommand and the first golden pin, unmodified. If it
// regresses every ordinary slot session would lose ANTHROPIC_AUTH_TOKEN.
func TestClaudeBuildSlotsTokenModeUnchanged(t *testing.T) {
	sc, err := claudeDriver{}.BuildSlots(slotTestInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if sc.HeaderToken != "" {
		t.Error("a token-mode build set HeaderToken")
	}
	if v, _ := lastEnv(sc.Env, "ANTHROPIC_AUTH_TOKEN"); v != slotTestKey {
		t.Errorf("token mode lost ANTHROPIC_AUTH_TOKEN: %v", sc.Env)
	}
	// BuildSlotCommand derives Subscription from the slot models; none here.
	sc2, err := BuildSlotCommand(slotTestLaunch(), false, "http://localhost:4000", slotTestKey)
	if err != nil {
		t.Fatal(err)
	}
	if sc2.HeaderToken != "" {
		t.Error("BuildSlotCommand set HeaderToken for a profile with no subscription model")
	}
}

// TestBuildSlotCommandDerivesSubscription pins that BuildSlotCommand (the
// launch path and `wt profile show`) turns header mode on when any slot model
// uses the subscription, an unfilled slot that holds the default's model
// included. If it regresses a mixed profile launches in token mode and every
// subscription request is a 401.
func TestBuildSlotCommandDerivesSubscription(t *testing.T) {
	sl := slotTestLaunch()
	sl.Models["haiku"] = subModel("claude-haiku-5-5")
	sc, err := BuildSlotCommand(sl, false, "http://localhost:4000", subTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if sc.HeaderToken != subTestToken {
		t.Error("a profile with a subscription slot did not build in header mode")
	}
	if _, ok := lastEnv(sc.Env, "ANTHROPIC_AUTH_TOKEN"); ok {
		t.Error("header mode set ANTHROPIC_AUTH_TOKEN")
	}
}

// TestClaudeSingleModelSubscription pins that `-M claude-opus-5-5` launches
// in header mode: no ANTHROPIC_AUTH_TOKEN, an empty ANTHROPIC_API_KEY, the
// token only in HeaderToken (and so in the assembled ANTHROPIC_CUSTOM_HEADERS),
// the stray credentials cleared, and the model's id on --model. A
// non-subscription model stays in token mode with no HeaderToken.
func TestClaudeSingleModelSubscription(t *testing.T) {
	m := subModel("claude-opus-5-5")
	r := GatewayRoute(testGW(), m)
	if !r.Subscription {
		t.Fatal("GatewayRoute dropped Subscription")
	}
	lc := claudeDriver{}.Build(m, false, r)
	if lc.HeaderToken != testGWToken {
		t.Error("HeaderToken is not the gateway token")
	}
	if _, ok := lastEnv(lc.Env, "ANTHROPIC_AUTH_TOKEN"); ok {
		t.Errorf("header mode set ANTHROPIC_AUTH_TOKEN: %v", lc.Env)
	}
	if v, ok := lastEnv(lc.Env, "ANTHROPIC_API_KEY"); !ok || v != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q (set %v), want empty and set", v, ok)
	}
	if v, _ := lastEnv(lc.Env, "ANTHROPIC_BASE_URL"); v != testGWOrigin {
		t.Errorf("ANTHROPIC_BASE_URL = %q", v)
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if !slices.Contains(lc.ClearEnv, k) {
			t.Errorf("ClearEnv lacks %s", k)
		}
	}
	if v, _ := argsFlagValue(lc.Args, "--model"); v != "claude-opus-5-5" {
		t.Errorf("--model = %q", v)
	}
	if strings.Contains(strings.Join(append(slices.Clone(lc.Args), lc.Env...), "\n"), testGWToken) {
		t.Error("the token is in Args or Env")
	}

	// The assembled command carries the token in the header variable alone.
	fakeBin(t, "claude")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-FAKE")
	cmd, _, err := BuildLaunchCmdGateway("claude", m, t.TempDir(), false, testGW(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != "x-wt-model-token: "+testGWToken {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS = %q", v)
	}
	if _, ok := lastEnv(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Error("an inherited CLAUDE_CODE_OAUTH_TOKEN reached the header-mode child")
	}
	for _, e := range cmd.Env {
		if strings.Contains(e, testGWToken) && !strings.HasPrefix(e, "ANTHROPIC_CUSTOM_HEADERS=") {
			t.Errorf("the token is in %s", strings.SplitN(e, "=", 2)[0])
		}
	}

	plain := claudeDriver{}.Build(gwCloudModel(), false, GatewayRoute(testGW(), gwCloudModel()))
	if plain.HeaderToken != "" {
		t.Error("a non-subscription model set HeaderToken")
	}
}

// TestMergeCustomHeaders pins the two pure helpers of decision 7 on one
// table: header mode (mergeCustomHeaders) keeps the user's own headers, drops
// a stale wt line and any authorization line (case-insensitive, trimmed) and
// appends the one wt line; every other launch (stripWtHeader) drops only the
// wt line and keeps authorization. \r\n line ends are read as \n. If it
// regresses a stale token or an inherited Authorization reaches the gateway
// beside the login, or a user's own header is lost.
func TestMergeCustomHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, in, merged, stripped string
	}{
		{"empty", "", "x-wt-model-token: T", ""},
		{"a user header", "x-litellm-tags: a", "x-litellm-tags: a\nx-wt-model-token: T", "x-litellm-tags: a"},
		{"a stale wt line", "X-WT-Model-Token: old", "x-wt-model-token: T", ""},
		{"an authorization line", "Authorization: x", "x-wt-model-token: T", "Authorization: x"},
		{"all three", "x-litellm-tags: a\nX-WT-Model-Token: old\nAuthorization: x", "x-litellm-tags: a\nx-wt-model-token: T", "x-litellm-tags: a\nAuthorization: x"},
		{"crlf line ends", "x-litellm-tags: a\r\n  x-wt-model-token : old\r\nauthorization: x\r\n", "x-litellm-tags: a\nx-wt-model-token: T", "x-litellm-tags: a\nauthorization: x"},
		{"lone cr line ends", "a: 1\rx-wt-model-token: old\rauthorization: x", "a: 1\nx-wt-model-token: T", "a: 1\nauthorization: x"},
		{"near-miss names are not the wt header", "x-wt-model-tokenx: v\nx-wt-model-token-extra: v\nx-wt-model-token",
			"x-wt-model-tokenx: v\nx-wt-model-token-extra: v\nx-wt-model-token\nx-wt-model-token: T",
			"x-wt-model-tokenx: v\nx-wt-model-token-extra: v\nx-wt-model-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeCustomHeaders(tc.in, "T"); got != tc.merged {
				t.Errorf("mergeCustomHeaders = %q, want %q", got, tc.merged)
			}
			if got := stripWtHeader(tc.in); got != tc.stripped {
				t.Errorf("stripWtHeader = %q, want %q", got, tc.stripped)
			}
		})
	}
}

// TestWtHeaderNameMatchesTheGateway pins the header name the launch writes
// to the one the gateway reads. Two spellings would make every header-mode
// request a 401.
func TestWtHeaderNameMatchesTheGateway(t *testing.T) {
	if profiles.WtHeaderName != modelgateway.TokenHeader {
		t.Errorf("profiles.WtHeaderName = %q, modelgateway.TokenHeader = %q", profiles.WtHeaderName, modelgateway.TokenHeader)
	}
}

// TestSlotLaunchCmdMergesInheritedHeaders pins the slot launch's env
// assembly in header mode: an inherited ANTHROPIC_CUSTOM_HEADERS keeps the
// user's header, loses its stale wt and authorization lines, gains the one wt
// line, and appears exactly once. ANTHROPIC_AUTH_TOKEN and
// CLAUDE_CODE_OAUTH_TOKEN are gone from the child.
func TestSlotLaunchCmdMergesInheritedHeaders(t *testing.T) {
	fakeBin(t, "claude")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-litellm-tags: a\nx-wt-model-token: old\nAuthorization: x")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-FAKE")
	in := subSlotInput()
	cmd, err := BuildSlotLaunchCmdGateway(in.SlotLaunch, t.TempDir(), false, Gateway{Origin: in.ProxyOrigin, Token: subTestToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := envCount(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); n != 1 {
		t.Fatalf("ANTHROPIC_CUSTOM_HEADERS appears %d times", n)
	}
	want := "x-litellm-tags: a\nx-wt-model-token: " + subTestToken
	if v, _ := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != want {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS = %q, want the merged value", v)
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, ok := lastEnv(cmd.Env, k); ok {
			t.Errorf("%s reached the header-mode child", k)
		}
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, subTestToken) {
			t.Error("the wt token reached argv")
		}
	}
}

// TestTokenModeStripsStaleWtHeader pins that a token-mode launch (a slot
// profile with no subscription model, and a single-model launch) removes
// only the inherited x-wt-model-token line and leaves everything else of
// ANTHROPIC_CUSTOM_HEADERS as it was; with only that line inherited the
// variable is removed. Without it a session started from a header-mode
// shell would send a stale wt token beside its own.
func TestTokenModeStripsStaleWtHeader(t *testing.T) {
	fakeBin(t, "claude")
	for _, tc := range []struct{ name, in, want string }{
		{"user headers kept", "x-litellm-tags: a\nx-wt-model-token: old\nauthorization: x", "x-litellm-tags: a\nauthorization: x"},
		{"only the wt line", "x-wt-model-token: old", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_CUSTOM_HEADERS", tc.in)
			slot, err := BuildSlotLaunchCmdGateway(slotTestLaunch(), t.TempDir(), false, Gateway{Origin: "http://localhost:4000", Token: slotTestKey}, nil)
			if err != nil {
				t.Fatal(err)
			}
			single, _, err := BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, testGW(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for name, cmd := range map[string]*exec.Cmd{"slot": slot, "single": single} {
				v, ok := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS")
				if tc.want == "" {
					if ok {
						t.Errorf("%s: ANTHROPIC_CUSTOM_HEADERS = %q, want it removed", name, v)
					}
					continue
				}
				if v != tc.want || envCount(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS") != 1 {
					t.Errorf("%s: ANTHROPIC_CUSTOM_HEADERS = %q, want %q once", name, v, tc.want)
				}
			}
		})
	}
	t.Run("no wt line: the variable is left exactly as inherited", func(t *testing.T) {
		t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-litellm-tags: a\r\n")
		cmd, _, err := BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, testGW(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != "x-litellm-tags: a\r\n" {
			t.Errorf("ANTHROPIC_CUSTOM_HEADERS = %q, want it untouched", v)
		}
	})
}

// TestNativeLaunchStripsInheritedWtHeader pins the nested case of decision
// 7: `wt -M claude/native` or `claude/opus` started from a header-mode
// session's shell would otherwise carry the wt token straight to
// api.anthropic.com.
func TestNativeLaunchStripsInheritedWtHeader(t *testing.T) {
	fakeBin(t, "claude")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-litellm-tags: a\nx-wt-model-token: "+subTestToken)
	for _, name := range []string{"native", "opus"} {
		t.Run(name, func(t *testing.T) {
			m := config.Model{ID: "claude/" + name, ProviderID: "claude", ModelName: name, Native: true}
			cmd, _, err := BuildLaunchCmdGateway("claude", m, t.TempDir(), false, testGW(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range cmd.Env {
				if strings.Contains(strings.ToLower(e), "x-wt-model-token") || strings.Contains(e, subTestToken) {
					t.Errorf("the native child env carries the wt header")
				}
			}
			if v, _ := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != "x-litellm-tags: a" {
				t.Errorf("ANTHROPIC_CUSTOM_HEADERS = %q, want the user's header alone", v)
			}
		})
	}
	t.Run("the zero native model", func(t *testing.T) {
		cmd, _, err := BuildLaunchCmdGateway("claude", NativeModel(), t.TempDir(), false, Gateway{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); strings.Contains(strings.ToLower(v), "x-wt-model-token") {
			t.Error("the zero native launch kept the wt header")
		}
	})
}

// TestEveryAgentStripsInheritedWtHeader pins the same rule for every other
// agent: pi and codex (and every driver built through command) never inherit
// the wt line, so a pi or codex session started from a header-mode shell
// cannot leak the token to a provider it reaches by itself.
func TestEveryAgentStripsInheritedWtHeader(t *testing.T) {
	piHomeModelsPath(t)
	for _, name := range []string{"pi", "codex", "copilot", "opencode"} {
		t.Run(name, func(t *testing.T) {
			fakeBin(t, name)
			t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-wt-model-token: "+subTestToken)
			for _, m := range []config.Model{gwCloudModel(), NativeModel()} {
				gw := testGW()
				if m.Native {
					gw = Gateway{}
				}
				cmd, _, err := BuildLaunchCmdGateway(name, m, t.TempDir(), false, gw, []config.Model{gwCloudModel()}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := lastEnv(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS"); ok {
					t.Errorf("%s (native %v) kept ANTHROPIC_CUSTOM_HEADERS holding only the wt line", name, m.Native)
				}
			}
		})
	}
}

// TestSlotProfileHeaderValueRefusedInHeaderMode pins the build-time half of
// decision 7's profile rule: a profile env or config_content.env naming
// ANTHROPIC_CUSTOM_HEADERS (even a harmless x-litellm-tags line, which static
// validation accepts) or CLAUDE_CODE_OAUTH_TOKEN is accepted in token mode
// and refused in header mode, naming the key and never its value: the
// --settings env outranks the process env, so it would replace the merged
// header (an opaque 401) or displace the checked login. Every accepted
// header-mode build carries neither key in its --settings env.
func TestSlotProfileHeaderValueRefusedInHeaderMode(t *testing.T) {
	const secret = "sk-ant-oat01-FAKEvalue"
	values := map[string]string{
		"ANTHROPIC_CUSTOM_HEADERS": "x-litellm-tags: a",
		"CLAUDE_CODE_OAUTH_TOKEN":  secret,
	}
	for key, val := range values {
		for _, where := range []string{"env", "config_content.env"} {
			t.Run(key+" in "+where, func(t *testing.T) {
				set := func(in *profiles.SlotBuildInput) {
					if where == "env" {
						in.Env = map[string]string{key: val}
					} else {
						in.ConfigContent = map[string]any{"env": map[string]any{key: val}}
					}
				}
				// Statically accepted.
				p := profiles.SlotProfile{Name: "p", Agent: "claude", Default: "opus", Slots: map[string]string{"opus": "claude-opus-5-5"}}
				if where == "env" {
					p.Env = map[string]string{key: val}
				} else {
					p.ConfigContent = map[string]any{"env": map[string]any{key: val}}
				}
				if c := profiles.ValidateSlots(profiles.SlotStore{Profiles: []profiles.SlotProfile{p}}, SlotDrivers()); c[0].Err != nil {
					t.Fatalf("static validation refused it: %v", c[0].Err)
				}
				// Token mode accepts it.
				tok := slotTestInput(false)
				tok.Env, tok.ConfigContent = nil, nil
				set(&tok)
				if _, err := (claudeDriver{}).BuildSlots(tok); err != nil {
					t.Fatalf("token mode refused it: %v", err)
				}
				// Header mode refuses it, naming the key only.
				sub := subSlotInput()
				set(&sub)
				_, err := claudeDriver{}.BuildSlots(sub)
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("header mode err = %v, want a refusal naming %s", err, key)
				}
				if strings.Contains(err.Error(), val) || strings.Contains(err.Error(), subTestToken) {
					t.Errorf("the refusal echoes a value: %v", err)
				}
			})
		}
	}
	t.Run("an accepted header-mode build", func(t *testing.T) {
		in := subSlotInput()
		in.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
		sc, err := claudeDriver{}.BuildSlots(in)
		if err != nil {
			t.Fatal(err)
		}
		env := settingsEnv(t, sc.Args)
		for _, k := range []string{"ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_OAUTH_TOKEN"} {
			if _, ok := env[k]; ok {
				t.Errorf("--settings env names %s", k)
			}
		}
	})
}

// TestHeaderTokenRejectsControlChars pins ErrBadHeaderToken: a token that
// holds a newline, a control character or anything outside printable ASCII
// (or is empty) is refused before a launch is built, since it would split
// or corrupt the ANTHROPIC_CUSTOM_HEADERS value; and the refusal never
// carries the token.
func TestHeaderTokenRejectsControlChars(t *testing.T) {
	for _, tok := range []string{"", "wt-SENTINEL\ntok", "wt-SENTINEL\rtok", "wt-SENTINEL\ttok", "wt-SENTINEL\x00tok", "wt-SENTINEL\x7ftok", "wt-SENTINEL-tök"} {
		if err := checkHeaderToken(tok); !errors.Is(err, ErrBadHeaderToken) {
			t.Errorf("checkHeaderToken(%q) = %v, want ErrBadHeaderToken", tok, err)
		} else if tok != "" && strings.Contains(err.Error(), "SENTINEL") {
			t.Errorf("the refusal carries the token: %v", err)
		}
		in := subSlotInput()
		in.ProxyKey = tok
		if _, err := (claudeDriver{}).BuildSlots(in); !errors.Is(err, ErrBadHeaderToken) {
			t.Errorf("BuildSlots with token %q = %v, want ErrBadHeaderToken", tok, err)
		}
	}
	if err := checkHeaderToken("wt-SENTINEL-tok_~!"); err != nil {
		t.Errorf("a printable token was refused: %v", err)
	}
	// The single-model path refuses at assembly.
	fakeBin(t, "claude")
	_, _, err := BuildLaunchCmdGateway("claude", subModel("claude-opus-5-5"), t.TempDir(), false, Gateway{Origin: testGWOrigin, Token: "wt-SENTINEL\ntok"}, nil, nil)
	if !errors.Is(err, ErrBadHeaderToken) || strings.Contains(err.Error(), "SENTINEL") {
		t.Errorf("single-model launch err = %v, want ErrBadHeaderToken without the token", err)
	}
}

// TestHeaderModeClearsProviderSwitchesAndKey pins the rest of decision 7's
// header-mode clear list: an inherited CLAUDE_CODE_USE_BEDROCK or
// CLAUDE_CODE_USE_VERTEX (any CLAUDE_CODE_USE_*) would send the session to
// AWS or GCP instead of the gateway, carrying ANTHROPIC_CUSTOM_HEADERS (the
// wt token) with it, so it never reaches a header-mode child; an inherited
// ANTHROPIC_API_KEY is removed, leaving only wt's empty entry. A token-mode
// launch leaves the switches alone, as before.
func TestHeaderModeClearsProviderSwitchesAndKey(t *testing.T) {
	fakeBin(t, "claude")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api-FAKE")
	in := subSlotInput()
	gw := Gateway{Origin: in.ProxyOrigin, Token: subTestToken}
	slot, err := BuildSlotLaunchCmdGateway(in.SlotLaunch, t.TempDir(), false, gw, nil)
	if err != nil {
		t.Fatal(err)
	}
	single, _, err := BuildLaunchCmdGateway("claude", subModel("claude-opus-5-5"), t.TempDir(), false, gw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, cmd := range map[string]*exec.Cmd{"slot": slot, "single": single} {
		for _, k := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
			if _, ok := lastEnv(cmd.Env, k); ok {
				t.Errorf("%s: %s reached the header-mode child", name, k)
			}
		}
		if n := envCount(cmd.Env, "ANTHROPIC_API_KEY"); n != 1 {
			t.Errorf("%s: ANTHROPIC_API_KEY appears %d times, want wt's one empty entry", name, n)
		}
		if v, _ := lastEnv(cmd.Env, "ANTHROPIC_API_KEY"); v != "" {
			t.Errorf("%s: ANTHROPIC_API_KEY is not empty", name)
		}
	}
	tokSlot, err := BuildSlotLaunchCmdGateway(slotTestLaunch(), t.TempDir(), false, Gateway{Origin: "http://localhost:4000", Token: slotTestKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokSingle, _, err := BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, testGW(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, cmd := range map[string]*exec.Cmd{"token slot": tokSlot, "token single": tokSingle} {
		if v, _ := lastEnv(cmd.Env, "CLAUDE_CODE_USE_BEDROCK"); v != "1" {
			t.Errorf("%s: token mode dropped CLAUDE_CODE_USE_BEDROCK", name)
		}
	}
}

// TestLoneCRHeaderLinesAreCleaned pins that a value whose lines end in a lone
// \r is read line by line too: header mode drops the stale wt line, and
// every other launch strips it. Split on \n alone, the whole value would be
// one line naming "a" and the stale token would survive.
func TestLoneCRHeaderLinesAreCleaned(t *testing.T) {
	fakeBin(t, "claude")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "a: 1\rx-wt-model-token: old")
	sub, _, err := BuildLaunchCmdGateway("claude", subModel("claude-opus-5-5"), t.TempDir(), false, testGW(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := lastEnv(sub.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != "a: 1\nx-wt-model-token: "+testGWToken {
		t.Errorf("header mode ANTHROPIC_CUSTOM_HEADERS = %q", strings.ReplaceAll(v, testGWToken, "<token>"))
	}
	tok, _, err := BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, testGW(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := lastEnv(tok.Env, "ANTHROPIC_CUSTOM_HEADERS"); v != "a: 1" {
		t.Errorf("token mode ANTHROPIC_CUSTOM_HEADERS = %q, want %q", v, "a: 1")
	}
}

// TestBuildSlotsHeaderModeRefusesGatewayKeys pins the build's backstop for
// the keys wt itself owns: in header mode a profile env or
// config_content.env that names ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY or
// ANTHROPIC_BASE_URL is refused, naming the key and never its value, even
// when static validation (which reserves them) was bypassed. Any of them in
// the --settings env would displace the login or redirect the session.
func TestBuildSlotsHeaderModeRefusesGatewayKeys(t *testing.T) {
	const val = "VALUE-SENTINEL"
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		for _, where := range []string{"env", "config_content.env"} {
			t.Run(key+" in "+where, func(t *testing.T) {
				in := subSlotInput()
				if where == "env" {
					in.Env = map[string]string{key: val}
				} else {
					in.ConfigContent = map[string]any{"env": map[string]any{key: val}}
				}
				_, err := claudeDriver{}.BuildSlots(in)
				if err == nil || !strings.Contains(err.Error(), where+" sets "+key) {
					t.Fatalf("err = %v, want a refusal naming %s in %s", err, key, where)
				}
				if strings.Contains(err.Error(), val) || strings.Contains(err.Error(), subTestToken) {
					t.Errorf("the refusal echoes a value: %v", err)
				}
			})
		}
	}
}

// TestLaunchInfoReportsHeaderMode pins LaunchInfo.HeaderMode, which the
// pre-launch guard (Task 6) reads: true for a single-model launch and a slot
// launch built in header mode, false for token mode and native launches.
func TestLaunchInfoReportsHeaderMode(t *testing.T) {
	fakeBin(t, "claude")
	_, info, err := BuildLaunchCmdGateway("claude", subModel("claude-opus-5-5"), t.TempDir(), false, testGW(), nil, nil)
	if err != nil || !info.HeaderMode {
		t.Errorf("subscription single-model: HeaderMode = %v, err = %v", info.HeaderMode, err)
	}
	_, info, err = BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, testGW(), nil, nil)
	if err != nil || info.HeaderMode {
		t.Errorf("token single-model: HeaderMode = %v, err = %v", info.HeaderMode, err)
	}
	_, info, err = BuildLaunchCmdGateway("claude", NativeModel(), t.TempDir(), false, Gateway{}, nil, nil)
	if err != nil || info.HeaderMode {
		t.Errorf("native: HeaderMode = %v, err = %v", info.HeaderMode, err)
	}
	in := subSlotInput()
	_, info, err = BuildSlotLaunchCmdGatewayInfo(in.SlotLaunch, t.TempDir(), false, Gateway{Origin: in.ProxyOrigin, Token: subTestToken}, nil)
	if err != nil || !info.HeaderMode {
		t.Errorf("subscription slot: HeaderMode = %v, err = %v", info.HeaderMode, err)
	}
	_, info, err = BuildSlotLaunchCmdGatewayInfo(slotTestLaunch(), t.TempDir(), false, Gateway{Origin: "http://localhost:4000", Token: slotTestKey}, nil)
	if err != nil || info.HeaderMode {
		t.Errorf("token slot: HeaderMode = %v, err = %v", info.HeaderMode, err)
	}
}
