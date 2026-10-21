package main

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestMaskKey verifies the one rule wt shows the proxy key by: nothing of a
// key of four characters or fewer, the last four of a longer one. `wt litellm
// status` and `wt profile show` both use it; a second rule in either place
// could print a short key whole.
func TestMaskKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"", "(unset)"},
		{"ab", "***"},
		{"abcd", "***"},
		{"abcde", "***bcde"},
		{"sk-abcdef1234", "***1234"},
	} {
		if got := maskKey(tc.key); got != tc.want {
			t.Errorf("maskKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// runProfileCmd runs `wt profile <args>` on a and returns its stdout and its
// error. cobra's own "Error:" line goes nowhere.
func runProfileCmd(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	cmd := profileCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestProfileListSlotSection verifies the slot-profile section of `wt profile
// list`: one line per profile with its agent, its id and "ok" or its static
// error; a profile with no usable name or agent listed by its position in the
// file; and the section printed whatever state profiles.toml is in, missing,
// failing validation or unparseable. It needs no running provider, so the
// inventory probe must not run. If the section depended on profiles.toml, a
// user with a broken automatic-profile file could not see why a slot profile
// is missing from the picker.
func TestProfileListSlotSection(t *testing.T) {
	ok := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	broken := slotFixtureCheck(1, "broken", "x", slotCloud())
	broken.Err = errors.New(`default "x" is not a slot this profile fills`)
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}
	checks := []profiles.SlotCheck{ok, broken, unusable}
	section := "slot profiles:\n" +
		"  claude: profile/cloud ok\n" +
		"  claude: profile/broken error: default \"x\" is not a slot this profile fills\n" +
		"  slot-profiles.toml[2]: error: name is missing\n"
	automatic := profiles.Store{Enabled: true, Profiles: []profiles.Profile{{Agent: "pi", Match: "location", Location: "local"}}}
	profilesErr := errors.New("parse profiles.toml: simulated parse failure")
	slotErr := errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")

	reservedProvider := slotFixtureConfig()
	reservedProvider.Providers = append(reservedProvider.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	reservedModel := slotFixtureConfig()
	reservedModel.Models = append(reservedModel.Models, config.Model{ID: "profile/mixed", ProviderID: "openrouter", ModelName: "mixed"})

	cases := []struct {
		name    string
		a       *app
		want    string // the whole of stdout
		wantErr string // "" means nil
	}{
		{"profiles.toml missing, no slot file",
			&app{profiles: profiles.Store{Enabled: true}},
			"(no profiles defined)\nslot profiles: (none defined)\n", ""},
		{"profiles.toml missing, slot profiles present",
			&app{profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section, ""},
		{"both files have profiles",
			&app{profiles: automatic, slotChecks: checks},
			"pi: match=location location=local\n" + section, ""},
		{"profiles.toml does not parse",
			&app{profilesLoadErr: profilesErr, slotChecks: checks},
			section, "profiles.toml: parse profiles.toml: simulated parse failure"},
		{"profiles.toml fails validation",
			&app{profiles: automatic, profilesValidateErr: errors.New("agent does not support mechanism \"wrapper\""), slotChecks: checks},
			"pi: match=location location=local\n" +
				"warning: profiles.toml failed validation — profiles are disabled for every launch: agent does not support mechanism \"wrapper\"\n" +
				section, ""},
		{"an unknown key and no profile",
			&app{profiles: profiles.Store{Enabled: true}, slotUnknown: []string{"profiles"}},
			"(no profiles defined)\nslot profiles: (none defined)\n" +
				"  warning: slot-profiles.toml: unknown key \"profiles\" — ignored\n", ""},
		{"an unknown key beside profiles",
			&app{profiles: profiles.Store{Enabled: true}, slotChecks: checks, slotUnknown: []string{"profile.envs"}},
			"(no profiles defined)\n" + section +
				"  warning: slot-profiles.toml: unknown key \"profile.envs\" — ignored\n", ""},
		{"the registry has a provider with id profile",
			&app{cfg: reservedProvider, profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section +
				"  warning: the registry has a provider with id \"profile\" — slot profiles are unavailable\n", ""},
		{"a registry model id begins with profile/",
			&app{cfg: reservedModel, profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section +
				"  warning: the registry has a model whose id begins with \"profile/\" (profile/mixed) — slot profiles are unavailable\n", ""},
		{"slot-profiles.toml does not parse",
			&app{profiles: profiles.Store{Enabled: true}, slotLoadErr: slotErr},
			"(no profiles defined)\nslot profiles: parse slot-profiles.toml: toml: line 2: unexpected EOF\n",
			"parse slot-profiles.toml: toml: line 2: unexpected EOF"},
		{"neither file parses",
			&app{profilesLoadErr: profilesErr, slotLoadErr: slotErr},
			"slot profiles: parse slot-profiles.toml: toml: line 2: unexpected EOF\n",
			"profiles.toml: parse profiles.toml: simulated parse failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := probeInventory
			probeInventory = func(*config.Config) localmodels.Snapshot {
				t.Error("`wt profile list` probed the providers")
				return localmodels.Snapshot{}
			}
			t.Cleanup(func() { probeInventory = old })

			out, err := runProfileCmd(t, tc.a, "list")

			if out != tc.want {
				t.Errorf("stdout =\n%s\nwant\n%s", out, tc.want)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// slotShowApp is an app holding the fixture config and three slot profiles:
// an all-cloud one, one whose local model is idle, and one naming a model
// that is not in the registry.
func slotShowApp() *app {
	return &app{
		cfg:      slotFixtureConfig(),
		profiles: profiles.Store{Enabled: true},
		slotChecks: []profiles.SlotCheck{
			slotFixtureCheck(0, "cloud", "opus", slotCloud()),
			slotFixtureCheck(1, "mixed", "opus", slotMixed()),
			slotFixtureCheck(2, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
			opusplanFixtureCheck(3, "planned"),
		},
	}
}

// opusplanFixtureCheck is a profile whose default is claude's opusplan
// alias, as ValidateSlots returns it: opus and sonnet filled, resting on
// sonnet.
func opusplanFixtureCheck(index int, name string) profiles.SlotCheck {
	c := slotFixtureCheck(index, name, "opusplan", map[string]string{"opus": "openrouter/b", "sonnet": "openrouter/a"})
	c.DefaultSlot = "sonnet"
	c.Alias, _ = agents.SlotDrivers().DefaultAlias("claude", "opusplan")
	return c
}

// TestProfileShowSlotProfile verifies `wt profile show -A claude -M
// profile/<name>` for a launch row, a start row and a blocked row. It prints
// the picker's verdict, each slot's registry id and provider-side name (the
// default marked, an unfilled slot shown with the default's model), and the
// command wt would run. The proxy key is masked everywhere. A blocked profile
// prints its reason and returns an error, so the command exits non-zero. It
// starts no model and touches no route: this is the command a user runs to
// find out why a profile will not launch, and it must be safe to run.
func TestProfileShowSlotProfile(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		want    []string // lines stdout must contain
		wantErr string   // "" means nil
	}{
		{"launch row", "profile/cloud", []string{
			"profile/cloud (claude): launch\n",
			"slot fable: openrouter/a (a) [default's model]\n",
			"slot opus*: openrouter/a (a)\n",
			"slot sonnet: openrouter/a (a) [default's model]\n",
			"slot haiku: openrouter/b (b)\n",
			"env: ANTHROPIC_BASE_URL=http://localhost:4000\n",
			"env: ANTHROPIC_AUTH_TOKEN=***1234\n",
			"env: ANTHROPIC_API_KEY=\n",
			"unset: ANTHROPIC_SMALL_FAST_MODEL\n",
			"unset: CLAUDE_CODE_SUBAGENT_MODEL\n",
			"unset: ANTHROPIC_DEFAULT_*\n",
			"args: --model opus --settings <settings>\n",
			"settings:\n  {\n",
			`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "openrouter/b"`,
			`"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/a"`,
		}, ""},
		{"start row", "profile/mixed", []string{
			"profile/mixed (claude): start\n",
			"slot opus*: openrouter/a (a)\n",
			"slot haiku: omlx/q (q)\n",
			"env: ANTHROPIC_AUTH_TOKEN=***1234\n",
			`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/q"`,
		}, ""},
		{"opusplan default", "profile/planned", []string{
			"profile/planned (claude): launch\n",
			"default: opusplan — the opus slot in plan mode, the sonnet slot otherwise\n",
			"slot fable: openrouter/a (a) [default's model]\n",
			"slot opus: openrouter/b (b)\n",
			"slot sonnet*: openrouter/a (a)\n",
			"slot haiku: openrouter/a (a) [default's model]\n",
			"args: --model opusplan --settings <settings>\n",
			`"CLAUDE_CODE_DISABLE_1M_CONTEXT": "1"`,
		}, ""},
		{"blocked row", "profile/broken", []string{
			"profile/broken (claude): blocked\n",
			"reason: slot opus: openrouter/gone is not in the registry\n",
		}, "profile/broken is blocked: slot opus: openrouter/gone is not in the registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seams := stubSlotSeams(t)
			started := stubStartDriver(t, nil)

			out, err := runProfileCmd(t, slotShowApp(), "show", "-A", "claude", "-M", tc.id)

			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q; stdout =\n%s", want, out)
				}
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(out, "sk-test-1234") {
				t.Errorf("the proxy key is printed:\n%s", out)
			}
			if tc.wantErr != "" && strings.Contains(out, "args:") {
				t.Errorf("a blocked profile printed a command:\n%s", out)
			}
			if seams.log() != "" || started.called {
				t.Errorf("`wt profile show` drove the engine: steps %q, start asked for: %v", seams.log(), started.called)
			}
		})
	}
}

// TestProfileShowSlotProfileErrors verifies the ways the slot branch of `wt
// profile show` says no: a name that is not defined, a profile of another
// agent, and a slot-profiles.toml that did not load. Each is an error, never
// an empty report that reads as "nothing would apply".
func TestProfileShowSlotProfileErrors(t *testing.T) {
	withPi := slotShowApp()
	withPi.cfg.Agents = append(withPi.cfg.Agents, config.Agent{Name: "pi", SupportedProviders: []string{"openrouter", "omlx"}})
	broken := slotShowApp()
	broken.slotChecks = nil
	broken.slotLoadErr = errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")
	cases := []struct {
		name    string
		a       *app
		agent   string
		id      string
		wantErr string
	}{
		{"unknown name", slotShowApp(), "claude", "profile/nope", `slot profile "profile/nope" is not defined for agent "claude"`},
		{"another agent's profile", withPi, "pi", "profile/cloud", `slot profile "profile/cloud" is not defined for agent "pi"`},
		{"the file did not load", broken, "claude", "profile/cloud", "parse slot-profiles.toml: toml: line 2: unexpected EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runProfileCmd(t, tc.a, "show", "-A", tc.agent, "-M", tc.id)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

// TestProfileShowSlotProfileIgnoresProfilesToml verifies the slot branch does
// not depend on profiles.toml: with that file unparseable, or failing
// validation, a slot profile is still shown. The two files are independent,
// and a broken automatic-profile file must not hide a working slot profile.
func TestProfileShowSlotProfileIgnoresProfilesToml(t *testing.T) {
	for name, damage := range map[string]func(*app){
		"profiles.toml does not parse": func(a *app) {
			a.profilesLoadErr = errors.New("parse profiles.toml: simulated parse failure")
		},
		"profiles.toml fails validation": func(a *app) {
			a.profilesValidateErr = errors.New("agent does not support mechanism \"wrapper\"")
		},
	} {
		t.Run(name, func(t *testing.T) {
			stubSlotSeams(t)
			a := slotShowApp()
			damage(a)
			out, err := runProfileCmd(t, a, "show", "-A", "claude", "-M", "profile/cloud")
			if err != nil || !strings.HasPrefix(out, "profile/cloud (claude): launch\n") {
				t.Errorf("show = (%q, %v), want the profile shown", out, err)
			}
		})
	}
}

// TestProfileCommandsReportAConfigContentEnvThatIsNotATable follows a file
// through the real pipeline (ParseSlots, ValidateSlots with the registered
// drivers) into `wt profile list` and `wt profile show`. A claude profile
// whose config_content.env is a string is listed with the static error, and
// show prints it as the block reason and exits non-zero without printing a
// command. The profile beside it, whose config_content.env is a table, is
// listed ok and shown as a launch row with the table's key in the settings.
// Without the static rule both commands said the first profile would launch,
// and the launch failed at the slot build.
func TestProfileCommandsReportAConfigContentEnvThatIsNotATable(t *testing.T) {
	const file = `
[[profile]]
name = "string-env"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content]
env = "x"

[[profile]]
name = "table-env"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content.env]
MAX_THINKING_TOKENS = "4096"
`
	store, err := profiles.ParseSlots([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	newApp := func() *app {
		return &app{cfg: slotFixtureConfig(), profiles: profiles.Store{Enabled: true},
			slotChecks: profiles.ValidateSlots(store, agents.SlotDrivers())}
	}
	seams := stubSlotSeams(t)
	started := stubStartDriver(t, nil)

	out, err := runProfileCmd(t, newApp(), "list")
	wantList := "(no profiles defined)\nslot profiles:\n" +
		"  claude: profile/string-env error: config_content.env must be a table\n" +
		"  claude: profile/table-env ok\n"
	if err != nil || out != wantList {
		t.Errorf("list = (%q, %v)\nwant %q, nil", out, err, wantList)
	}

	out, err = runProfileCmd(t, newApp(), "show", "-A", "claude", "-M", "profile/string-env")
	wantShow := "profile/string-env (claude): blocked\nreason: config_content.env must be a table\n"
	const wantErr = "profile/string-env is blocked: config_content.env must be a table"
	if out != wantShow || err == nil || err.Error() != wantErr {
		t.Errorf("show string-env = (%q, %v)\nwant %q, %q", out, err, wantShow, wantErr)
	}

	out, err = runProfileCmd(t, newApp(), "show", "-A", "claude", "-M", "profile/table-env")
	if err != nil || !strings.HasPrefix(out, "profile/table-env (claude): launch\n") || !strings.Contains(out, `"MAX_THINKING_TOKENS": "4096"`) {
		t.Errorf("show table-env = (%q, %v), want a launch row whose settings carry the table", out, err)
	}
	if seams.log() != "" || started.called {
		t.Errorf("the profile commands drove the engine: steps %q, start asked for: %v", seams.log(), started.called)
	}
}

// TestProfileCommandsReportModelOverrides follows config_content.modelOverrides
// through the real drivers into `wt profile list`, `wt profile show` and a
// launch's warnings (#412, #419): a modelOverrides that is not a table blocks
// the profile, listed with the reason and shown blocked; a table whose targets
// are not the profile's slot models is listed ok with one warning line per
// target, naming the profile, the key and the target, and shown as a launch
// row with the same lines, each once, right under its slot lines, its
// overrides in the settings. A table whose targets are all slot models has no
// warning, in either command, and a blocked profile is shown with its reason
// alone even when its overrides would warn. Without the block, a string
// modelOverrides reached Claude Code's settings, which take an object; without
// the warning, an override to an unrouted id failed only inside the session,
// and `wt profile show`, the command a user runs to see what a launch would
// send, said nothing about it (#419).
func TestProfileCommandsReportModelOverrides(t *testing.T) {
	const file = `
[[profile]]
name = "string-overrides"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content]
modelOverrides = "openrouter/a"

[[profile]]
name = "unrouted"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content.modelOverrides]
claude-sonnet-4-6 = "openrouter/b"
claude-haiku-4-5 = "omlx/q"

[[profile]]
name = "routed"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
haiku = "openrouter/b"
[profile.config_content.modelOverrides]
claude-sonnet-4-6 = "openrouter/b"

[[profile]]
name = "blocked-unrouted"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content]
env = "x"
[profile.config_content.modelOverrides]
claude-sonnet-4-6 = "openrouter/b"
`
	store, err := profiles.ParseSlots([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	newApp := func() *app {
		return &app{cfg: slotFixtureConfig(), profiles: profiles.Store{Enabled: true},
			slotChecks: profiles.ValidateSlots(store, agents.SlotDrivers())}
	}
	seams := stubSlotSeams(t)
	started := stubStartDriver(t, nil)
	warnings := []string{
		`profile/unrouted (claude): config_content.modelOverrides["claude-haiku-4-5"] targets "omlx/q", which is not one of the profile's slot models, so wt does not make sure it is routed`,
		`profile/unrouted (claude): config_content.modelOverrides["claude-sonnet-4-6"] targets "openrouter/b", which is not one of the profile's slot models, so wt does not make sure it is routed`,
	}

	out, err := runProfileCmd(t, newApp(), "list")
	wantList := "(no profiles defined)\nslot profiles:\n" +
		"  claude: profile/string-overrides error: config_content.modelOverrides must be a table\n" +
		"  claude: profile/unrouted ok\n" +
		"  claude: profile/routed ok\n" +
		"  claude: profile/blocked-unrouted error: config_content.env must be a table\n" +
		"  warning: " + warnings[0] + "\n" +
		"  warning: " + warnings[1] + "\n"
	if err != nil || out != wantList {
		t.Errorf("list = (%q, %v)\nwant %q, nil", out, err, wantList)
	}

	for _, tc := range []struct{ name, reason string }{
		{"string-overrides", "config_content.modelOverrides must be a table"},
		{"blocked-unrouted", "config_content.env must be a table"},
	} {
		id := "profile/" + tc.name
		out, err = runProfileCmd(t, newApp(), "show", "-A", "claude", "-M", id)
		wantShow := id + " (claude): blocked\nreason: " + tc.reason + "\n"
		wantErr := id + " is blocked: " + tc.reason
		if out != wantShow || err == nil || err.Error() != wantErr {
			t.Errorf("show %s = (%q, %v)\nwant %q, %q", tc.name, out, err, wantShow, wantErr)
		}
	}

	out, err = runProfileCmd(t, newApp(), "show", "-A", "claude", "-M", "profile/unrouted")
	if err != nil || !strings.HasPrefix(out, "profile/unrouted (claude): launch\n") || !strings.Contains(out, `"claude-sonnet-4-6": "openrouter/b"`) {
		t.Errorf("show unrouted = (%q, %v), want a launch row whose settings carry the override", out, err)
	}
	lines := strings.Split(out, "\n")
	var got []string
	first := -1
	for i, line := range lines {
		if note, ok := strings.CutPrefix(line, "warning: "); ok {
			if first < 0 {
				first = i
			}
			got = append(got, note)
		}
	}
	if !slices.Equal(got, warnings) {
		t.Errorf("show unrouted warning lines = %q\nwant %q\nin %q", got, warnings, out)
	} else if !strings.HasPrefix(lines[first-1], "slot ") || !strings.HasPrefix(lines[first+len(got)], "env: ") {
		t.Errorf("show unrouted: the warnings are not right under the slot lines:\n%s", out)
	}

	out, err = runProfileCmd(t, newApp(), "show", "-A", "claude", "-M", "profile/routed")
	if err != nil || !strings.HasPrefix(out, "profile/routed (claude): launch\n") || strings.Contains(out, "warning:") {
		t.Errorf("show routed = (%q, %v), want a launch row with no warning", out, err)
	}

	want := []string{"wt: " + warnings[0], "wt: " + warnings[1]}
	if got := slotWarnings(newApp()); !slices.Equal(got, want) {
		t.Errorf("slotWarnings() = %q\nwant %q", got, want)
	}
	if seams.log() != "" || started.called {
		t.Errorf("the profile commands drove the engine: steps %q, start asked for: %v", seams.log(), started.called)
	}
}
