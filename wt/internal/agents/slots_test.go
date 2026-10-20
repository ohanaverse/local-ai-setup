package agents

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestSlotsFor pins which agents have slot support. If claude loses it every
// claude slot profile is reported as "does not support slot profiles"; if
// another agent gains it by accident its profiles pass static validation and
// fail at launch.
func TestSlotsFor(t *testing.T) {
	for _, tc := range []struct {
		agent string
		want  bool
	}{
		{"claude", true},
		{"codex", false},
		{"opencode", false},
		{"shell", false},
		{"no-such-agent", false},
	} {
		sc, ok := SlotsFor(tc.agent)
		if ok != tc.want {
			t.Errorf("SlotsFor(%q) ok = %v, want %v", tc.agent, ok, tc.want)
		}
		if ok && sc == nil {
			t.Errorf("SlotsFor(%q) returned ok with a nil value", tc.agent)
		}
	}
}

// TestSlotDriversAnswersFromTheRegisteredDrivers pins each callback static
// validation calls. If one is wired to the wrong driver capability a valid
// profile is blocked, or an invalid one reaches the launch.
func TestSlotDriversAnswersFromTheRegisteredDrivers(t *testing.T) {
	d := SlotDrivers()
	if !d.Known("claude") || !d.Known("codex") || d.Known("no-such-agent") {
		t.Errorf("Known: claude=%v codex=%v no-such-agent=%v, want true true false",
			d.Known("claude"), d.Known("codex"), d.Known("no-such-agent"))
	}
	if got, want := d.Slots("claude"), []string{"fable", "opus", "sonnet", "haiku"}; !slices.Equal(got, want) {
		t.Errorf("Slots(claude) = %v, want %v", got, want)
	}
	if got := d.Slots("codex"); got != nil {
		t.Errorf("Slots(codex) = %v, want nil (no slot support)", got)
	}
	if got := d.Slots("no-such-agent"); got != nil {
		t.Errorf("Slots(no-such-agent) = %v, want nil", got)
	}
	if got := d.ReservedEnv("claude"); len(got) != 11 || !slices.Contains(got, "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME") {
		t.Errorf("ReservedEnv(claude) = %v, want claude's eleven keys", got)
	}
	if got := d.ReservedEnv("codex"); got != nil {
		t.Errorf("ReservedEnv(codex) = %v, want nil", got)
	}
	if got, want := d.Mechanisms("claude"), []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile, profiles.MechanismConfigDefaults}; !slices.Equal(got, want) {
		t.Errorf("Mechanisms(claude) = %v, want %v", got, want)
	}
	if got := d.Mechanisms("no-such-agent"); got != nil {
		t.Errorf("Mechanisms(no-such-agent) = %v, want nil", got)
	}
	// pi is the one driver with a required partner: env needs a wrapper.
	if need, ok := d.Required("pi", profiles.MechanismEnv); !ok || need != profiles.MechanismWrapper {
		t.Errorf("Required(pi, env) = %q, %v, want wrapper, true", need, ok)
	}
	if _, ok := d.Required("claude", profiles.MechanismEnv); ok {
		t.Error("Required(claude, env) reported a partner; claude has none")
	}
	if _, ok := d.Required("no-such-agent", profiles.MechanismEnv); ok {
		t.Error("Required(no-such-agent, env) reported a partner")
	}
	// The config_content rule is claude's own: the same content is refused
	// for claude and passes for an agent whose driver has no such rule.
	notATable := map[string]any{"env": "x"}
	if err := d.ConfigContent("claude", notATable); err == nil || err.Error() != "config_content.env must be a table" {
		t.Errorf("ConfigContent(claude, env = \"x\") = %v, want \"config_content.env must be a table\"", err)
	}
	if err := d.ConfigContent("claude", map[string]any{"env": map[string]any{"A": "1"}}); err != nil {
		t.Errorf("ConfigContent(claude, env table) = %v, want nil", err)
	}
	for _, agent := range []string{"codex", "no-such-agent"} {
		if err := d.ConfigContent(agent, notATable); err != nil {
			t.Errorf("ConfigContent(%s, env = \"x\") = %v, want nil: the rule is claude's", agent, err)
		}
	}
}

// TestSlotFixtureIsValidForTheRealClaudeDriver reads the documented sample
// file and validates it against the registered drivers, not test closures.
// If the driver's slot names, mechanisms or reserved keys drift from the
// documented format, a user who copies the sample gets blocked rows.
func TestSlotFixtureIsValidForTheRealClaudeDriver(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/slot-profiles.sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	store, err := profiles.ParseSlots(data)
	if err != nil {
		t.Fatal(err)
	}
	checks := profiles.ValidateSlots(store, SlotDrivers())
	if len(checks) != 3 {
		t.Fatalf("got %d checks, want 3 (the fixture's three profiles)", len(checks))
	}
	if got := checks[2]; got.Profile.Default != "opusplan" || got.RestSlot() != "sonnet" {
		t.Errorf("profile %q: default %q rests on %q, want opusplan resting on sonnet", got.Profile.Name, got.Profile.Default, got.RestSlot())
	}
	for _, c := range checks {
		if !c.Usable || c.Err != nil {
			t.Errorf("profile %q: Usable = %v, Err = %v; want a valid profile", c.Profile.Name, c.Usable, c.Err)
		}
		if want := []string{"fable", "opus", "sonnet", "haiku"}; !slices.Equal(c.Declared, want) {
			t.Errorf("profile %q: Declared = %v, want %v", c.Profile.Name, c.Declared, want)
		}
	}
}

// TestSlotDriversRejectsWhatClaudeDoesNotAllow runs static validation with
// the real drivers over the profiles only the driver can refuse. If it
// regresses, a claude profile with args, a reserved env key, an unknown slot,
// a config_content.env or config_content.modelOverrides that is not a table
// or a nan under config_content passes validation, and its launch drops the setting silently, overrides
// the slot mapping or fails at the slot build after the picker offered it as
// a launch row.
func TestSlotDriversRejectsWhatClaudeDoesNotAllow(t *testing.T) {
	const head = "[[profile]]\nname = \"p\"\nagent = \"claude\"\ndefault = \"opus\"\n"
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"args",
			head + "args = [\"--verbose\"]\n[profile.slots]\nopus = \"ollama/a\"\n",
			`agent "claude" does not support mechanism "args"`},
		{"reserved env key",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.env]\nANTHROPIC_DEFAULT_OPUS_MODEL = \"x\"\n",
			"env sets ANTHROPIC_DEFAULT_OPUS_MODEL, which wt sets for the launch"},
		{"unknown slot",
			head + "[profile.slots]\nopus = \"ollama/a\"\nturbo = \"ollama/a\"\n",
			`slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`},
		{"config_content.env is a string",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.config_content]\nenv = \"x\"\n",
			"config_content.env must be a table"},
		{"config_content.modelOverrides is a string",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.config_content]\nmodelOverrides = \"ollama/a\"\n",
			"config_content.modelOverrides must be a table"},
		{"config_content.modelOverrides is an array",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.config_content]\nmodelOverrides = [\"ollama/a\"]\n",
			"config_content.modelOverrides must be a table"},
		{"config_content.env holds a nan",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.config_content.env]\nTHRESHOLD = nan\n",
			"config_content cannot be encoded as the --settings JSON: json: unsupported value: NaN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := profiles.ParseSlots([]byte(tc.toml))
			if err != nil {
				t.Fatal(err)
			}
			checks := profiles.ValidateSlots(store, SlotDrivers())
			if len(checks) != 1 || checks[0].Err == nil || checks[0].Err.Error() != tc.want {
				t.Fatalf("checks = %+v, want one check with Err %q", checks, tc.want)
			}
		})
	}
}

// TestCheckSlotPassthrough pins which arguments after `--` a claude slot
// profile refuses, in the two-argument form and in the "=" form. Either flag
// would replace the slot mapping, so a regression lets a profile session run
// on models the profile never chose. Flags that only share a prefix
// (--fallback-model) and agents that refuse nothing pass.
func TestCheckSlotPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		args  []string
		want  string // "" means no error; otherwise the flag the error names
	}{
		{"no args", "claude", nil, ""},
		{"harmless flags", "claude", []string{"--continue", "--verbose"}, ""},
		{"settings, two arguments", "claude", []string{"--settings", "x.json"}, "--settings"},
		{"settings, = form", "claude", []string{"--settings=x.json"}, "--settings"},
		{"model, two arguments", "claude", []string{"--model", "opus"}, "--model"},
		{"model, = form", "claude", []string{"--model=opus"}, "--model"},
		{"after other flags", "claude", []string{"--continue", "--model=opus"}, "--model"},
		{"the first refused flag is named", "claude", []string{"--model", "opus", "--settings", "x.json"}, "--model"},
		{"a flag that only shares a prefix", "claude", []string{"--fallback-model", "sonnet", "--setting-sources", "user"}, ""},
		{"an agent that refuses nothing", "codex", []string{"--model", "x"}, ""},
		{"an unknown agent", "no-such-agent", []string{"--model", "x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSlotPassthrough(tc.agent, tc.args)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			want := "a slot profile sets " + tc.want + " itself: remove it from the arguments after --"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

// slotTestCfg is a config whose [litellm] table has a url and a key and whose
// toggle is OFF: the slot path must use the proxy anyway. It is built in
// memory, so no test here reads the config home.
func slotTestCfg() *config.Config {
	cfg := &config.Config{}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: false, URL: "http://localhost:4000/", APIKey: slotTestKey})
	return cfg
}

// fakeSlotClaude puts an executable named claude first on PATH and returns
// its path.
func fakeSlotClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// envValues returns every value env holds for key, in order.
func envValues(env []string, key string) []string {
	var out []string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

// TestFilterEnvPrefixes pins the prefix filter: it drops a variable whose
// name starts with a prefix, keeps one that only contains it or carries it in
// its value, and keeps everything when there are no prefixes. If it regresses
// a stray ANTHROPIC_DEFAULT_* export survives into a profile session.
func TestFilterEnvPrefixes(t *testing.T) {
	env := []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL=stray",
		"ANTHROPIC_DEFAULT_=bare",
		"MY_ANTHROPIC_DEFAULT_X=keep",
		"NOTE=ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_BASE_URL=keep",
		"NOVALUE",
	}
	got := filterEnvPrefixes(env, []string{"ANTHROPIC_DEFAULT_"})
	want := []string{"MY_ANTHROPIC_DEFAULT_X=keep", "NOTE=ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_BASE_URL=keep", "NOVALUE"}
	if !slices.Equal(got, want) {
		t.Errorf("filterEnvPrefixes = %v, want %v", got, want)
	}
	if got := filterEnvPrefixes(env, nil); !slices.Equal(got, env) {
		t.Errorf("filterEnvPrefixes with no prefixes = %v, want the input unchanged", got)
	}
}

// TestBuildSlotCommand pins the build `wt profile show` prints: the driver's
// command, with whatever key the caller passes and no binary lookup. If it
// regresses, `wt profile show` either needs claude installed or prints
// something other than what a launch runs.
func TestBuildSlotCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no claude anywhere: the build must not look
	sc, err := BuildSlotCommand(slotTestLaunch(), true, "http://localhost:4000", "…test")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Bin != "claude" || sc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("command = %s %v, want claude with the yolo flag first", sc.Bin, sc.Args)
	}
	if got := envValues(sc.Env, "ANTHROPIC_AUTH_TOKEN"); !slices.Equal(got, []string{"…test"}) {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %v, want the key the caller passed", got)
	}

	sl := slotTestLaunch()
	sl.Agent = "codex"
	if _, err := BuildSlotCommand(sl, false, "http://localhost:4000", slotTestKey); err == nil || err.Error() != `agent "codex" does not support slot profiles` {
		t.Errorf("err = %v, want the no-slot-support error", err)
	}
}

// TestBuildSlotLaunchCmd pins the treatment a slot-profile command gets, the
// same a single-model launch gets: the binary found on PATH, the stray model
// variables removed from the inherited environment, the proxy variables set
// from [litellm] although its toggle is off, the worktree as directory and
// PWD, and the user's arguments appended unchanged. If it regresses a profile
// session opens the caller's directory, follows a shell export to a model the
// profile never chose, or drops the user's flags. It also pins that building
// writes nothing into the worktree.
func TestBuildSlotLaunchCmd(t *testing.T) {
	bin := fakeSlotClaude(t)
	work := t.TempDir()
	t.Setenv("PWD", "/stale/caller")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "stray-opus")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME", "stray-name")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "stray-fast")
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL", "stray-subagent")
	t.Setenv("ANTHROPIC_BASE_URL", "http://stale.invalid")
	t.Setenv("WT_SLOT_TEST_KEEP", "1")

	cmd, err := BuildSlotLaunchCmd(slotTestLaunch(), work, true, slotTestCfg(), []string{"--continue", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != bin {
		t.Errorf("Path = %q, want the claude on PATH %q", cmd.Path, bin)
	}
	if cmd.Dir != work {
		t.Errorf("Dir = %q, want %q", cmd.Dir, work)
	}
	if got := envValues(cmd.Env, "PWD"); !slices.Equal(got, []string{work}) {
		t.Errorf("PWD entries = %v, want exactly %q", got, work)
	}

	// cmd.Args[0] is the binary; then wt's flags, the payload, the user's args.
	wantHead := []string{"--dangerously-skip-permissions", "--model", "sonnet", "--settings"}
	args := cmd.Args[1:]
	if len(args) != len(wantHead)+3 || !slices.Equal(args[:len(wantHead)], wantHead) {
		t.Fatalf("args = %v, want %v, the payload, then the passthrough args", args, wantHead)
	}
	if got, want := args[len(args)-2:], []string{"--continue", "--verbose"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %v, want the passthrough args %v unchanged", got, want)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, slotTestKey) {
			t.Errorf("the proxy key reached argv: %s", a)
		}
	}

	for _, key := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		assertEnvMissing(t, cmd.Env, key)
	}
	assertEnv(t, cmd.Env, "WT_SLOT_TEST_KEEP", "1")
	// An inherited value of a variable wt sets is still in the list; the
	// child sees the last one. The url's trailing slash is trimmed.
	if got := envValues(cmd.Env, "ANTHROPIC_AUTH_TOKEN"); len(got) == 0 || got[len(got)-1] != slotTestKey {
		t.Errorf("ANTHROPIC_AUTH_TOKEN values = %v, want the last to be the [litellm] key", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_BASE_URL"); len(got) == 0 || got[len(got)-1] != "http://localhost:4000" {
		t.Errorf("ANTHROPIC_BASE_URL values = %v, want the last to be the proxy origin", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_API_KEY"); len(got) == 0 || got[len(got)-1] != "" {
		t.Errorf("ANTHROPIC_API_KEY values = %v, want the last to be empty", got)
	}

	env, _ := slotSettings(t, args)["env"].(map[string]any)
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "omlx/mlx-community--Qwen3.8-27B-4bit" {
		t.Errorf("payload haiku model = %v, want the profile's", got)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://localhost:4000" {
		t.Errorf("payload ANTHROPIC_BASE_URL = %v, want the proxy origin", got)
	}

	left, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the build wrote into the worktree: %v", left)
	}
}

// TestBuildSlotLaunchCmdRefusesPassthrough pins that --settings or --model
// after `--` stops the build, in the "=" form exactly as in the two-argument
// form, with the flag named, before anything else is looked at: there is no
// claude on PATH here and the refusal still comes first. If it regresses a
// user's `-- --model=opus` silently replaces the profile's default slot.
func TestBuildSlotLaunchCmdRefusesPassthrough(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
		flag string
	}{
		{"settings, two arguments", []string{"--settings", "x.json"}, "--settings"},
		{"settings, = form", []string{"--settings=x.json"}, "--settings"},
		{"model, two arguments", []string{"--model", "opus"}, "--model"},
		{"model, = form", []string{"--model=opus"}, "--model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := BuildSlotLaunchCmd(slotTestLaunch(), t.TempDir(), false, slotTestCfg(), tc.args)
			want := "a slot profile sets " + tc.flag + " itself: remove it from the arguments after --"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if cmd != nil {
				t.Errorf("a refused launch built a command: %v", cmd.Args)
			}
		})
	}
}

// TestBuildSlotLaunchCmdErrors pins every other reason no command is built:
// [litellm] without a url or a key (whatever its toggle says), an agent
// without slot support, and a missing binary. If one regresses, a profile
// launch starts an agent that cannot reach the proxy, or fails with an
// unreadable exec error.
func TestBuildSlotLaunchCmdErrors(t *testing.T) {
	withLitellm := func(s config.LitellmState) *config.Config {
		cfg := &config.Config{}
		cfg.SetLitellmForTest(s)
		return cfg
	}
	codex := slotTestLaunch()
	codex.Agent = "codex"
	for _, tc := range []struct {
		name         string
		sl           profiles.SlotLaunch
		cfg          *config.Config
		installed    bool
		want         string
		unconfigured bool
	}{
		{"no url, toggle on", slotTestLaunch(), withLitellm(config.LitellmState{Enabled: true, APIKey: slotTestKey}), true,
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"no key, toggle on", slotTestLaunch(), withLitellm(config.LitellmState{Enabled: true, URL: "http://localhost:4000"}), true,
			"a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"nil config", slotTestLaunch(), nil, true,
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"agent without slot support", codex, slotTestCfg(), true, `agent "codex" does not support slot profiles`, false},
		{"claude not installed", slotTestLaunch(), slotTestCfg(), false, "agent claude not installed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.installed {
				fakeSlotClaude(t)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			cmd, err := BuildSlotLaunchCmd(tc.sl, t.TempDir(), false, tc.cfg, nil)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got := errors.Is(err, config.ErrLitellmUnconfigured); got != tc.unconfigured {
				t.Errorf("errors.Is(err, config.ErrLitellmUnconfigured) = %v, want %v", got, tc.unconfigured)
			}
			if cmd != nil {
				t.Errorf("a failed build returned a command: %v", cmd.Args)
			}
		})
	}
}

// TestSlotDriversAcceptsAConfigContentEnvTable is the passing side of the
// config_content.env rule, through the real drivers: a table of ordinary
// keys passes static validation. If the rule refused every env key, no
// claude profile could set a variable through config_content.
func TestSlotDriversAcceptsAConfigContentEnvTable(t *testing.T) {
	store, err := profiles.ParseSlots([]byte("[[profile]]\nname = \"p\"\nagent = \"claude\"\ndefault = \"opus\"\n" +
		"[profile.slots]\nopus = \"ollama/a\"\n[profile.config_content.env]\nMAX_THINKING_TOKENS = \"4096\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	checks := profiles.ValidateSlots(store, SlotDrivers())
	if len(checks) != 1 || !checks[0].Usable || checks[0].Err != nil {
		t.Fatalf("checks = %+v, want one passing check", checks)
	}
}

// TestSlotConfigWarningsThroughTheRealDrivers follows a claude profile whose
// modelOverrides names an id it does not fill a slot with, from the file to
// the warning: static validation passes it (the warning never blocks), and
// SlotConfigWarnings says it once, naming the key and the target. The slot
// models are every id the profile fills a slot with, the default's among
// them, so a target that is one of them says nothing. A profile that failed
// validation, one that is not Usable and an agent without the capability say
// nothing either: the first two already carry their error. If the warning
// blocked, a profile overriding to an id the user routes by hand could not
// launch; if it were missing, the override would fail only inside the session.
func TestSlotConfigWarningsThroughTheRealDrivers(t *testing.T) {
	store, err := profiles.ParseSlots([]byte("[[profile]]\nname = \"p\"\nagent = \"claude\"\ndefault = \"opus\"\n" +
		"[profile.slots]\nopus = \"ollama/a\"\nhaiku = \"ollama/b\"\n" +
		"[profile.config_content.modelOverrides]\nclaude-opus-4-1 = \"ollama/a\"\nclaude-haiku-4-5 = \"ollama/b\"\nclaude-sonnet-4-6 = \"ollama/z\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	checks := profiles.ValidateSlots(store, SlotDrivers())
	if len(checks) != 1 || !checks[0].Usable || checks[0].Err != nil {
		t.Fatalf("checks = %+v, want one passing check", checks)
	}
	want := []string{`config_content.modelOverrides["claude-sonnet-4-6"] targets "ollama/z", which is not one of the profile's slot models, so wt does not make sure it is routed`}
	if got := SlotConfigWarnings(checks[0]); !slices.Equal(got, want) {
		t.Errorf("SlotConfigWarnings = %q\nwant %q", got, want)
	}

	failed := checks[0]
	failed.Err = errors.New("default is missing")
	unusable := checks[0]
	unusable.Usable = false
	codex := checks[0]
	codex.Profile.Agent = "codex"
	for name, c := range map[string]profiles.SlotCheck{"failed": failed, "unusable": unusable, "codex": codex} {
		if got := SlotConfigWarnings(c); got != nil {
			t.Errorf("%s: SlotConfigWarnings = %q, want nil", name, got)
		}
	}
}
