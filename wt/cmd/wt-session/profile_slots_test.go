package main

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

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
// failing validation or unparseable. It needs no running provider (this
// binary has no inventory probe to run). If the section depended on
// profiles.toml, a user with a broken automatic-profile file could not see
// why a slot profile is missing from the picker.
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
		// A session reads no registry (Task 18): a host registry holding the
		// "profile/" prefix says nothing here; the model server's catalog
		// blocks the profile's launch and `wt profile show` instead.
		{"the host registry has a provider with id profile",
			&app{hostSet: true, hostCfg: reservedProvider, profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section, ""},
		{"a host registry model id begins with profile/",
			&app{hostSet: true, hostCfg: reservedModel, profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section, ""},
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
func slotShowApp(t *testing.T) *app {
	t.Helper()
	a := pointAtFake(t, slotShowServer(t), "")
	a.profiles = profiles.Store{Enabled: true}
	a.slotChecks = []profiles.SlotCheck{
		slotFixtureCheck(0, "cloud", "opus", slotCloud()),
		slotFixtureCheck(1, "mixed", "opus", slotMixed()),
		slotFixtureCheck(2, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
		opusplanFixtureCheck(3, "planned"),
	}
	return a
}

// slotShowServer is the model server `wt profile show` reads a slot profile
// against: the slot fixture's two cloud models, launchable, and its local
// omlx/q, stopped (a start row); its token is "sk-test-1234", so the preview
// masks it as ***1234. slotShowOrigin is its address.
func slotShowServer(t *testing.T) *fakeModelServer {
	t.Helper()
	f := newFakeModelServer(t,
		modelapi.Row{ID: "openrouter/a", GatewayID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Location: "cloud",
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 1},
		modelapi.Row{ID: "openrouter/b", GatewayID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Location: "cloud",
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 2},
		localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	f.Token = "sk-test-1234"
	slotShowOrigin = f.URL
	return f
}

// slotShowOrigin is the address of the last slotShowServer.
var slotShowOrigin string

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
// profile/<name>` for a launch row, a start row and a blocked row, read
// against the model server's catalog. It prints the picker's verdict, each
// slot's model id and provider-side name (the default marked, an unfilled
// slot shown with the default's model), and the command wt would run against
// the gateway. The session token is masked everywhere. A blocked profile
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
			"env: ANTHROPIC_BASE_URL={{origin}}\n",
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
			"reason: slot opus: openrouter/gone is not in the model server's catalog\n",
		}, "profile/broken is blocked: slot opus: openrouter/gone is not in the model server's catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runProfileCmd(t, slotShowApp(t), "show", "-A", "claude", "-M", tc.id)

			for _, want := range tc.want {
				want = strings.ReplaceAll(want, "{{origin}}", slotShowOrigin)
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
		})
	}
}

// TestProfileShowSlotProfileErrors verifies the ways the slot branch of `wt
// profile show` says no: a name that is not defined, a profile of another
// agent, and a slot-profiles.toml that did not load. Each is an error, never
// an empty report that reads as "nothing would apply".
func TestProfileShowSlotProfileErrors(t *testing.T) {
	withPi := slotShowApp(t)
	hostCfgOf(withPi).Agents = append(hostCfgOf(withPi).Agents, config.Agent{Name: "pi", SupportedProviders: []string{"openrouter", "omlx"}})
	broken := slotShowApp(t)
	broken.slotChecks = nil
	broken.slotLoadErr = errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")
	cases := []struct {
		name    string
		a       *app
		agent   string
		id      string
		wantErr string
	}{
		{"unknown name", slotShowApp(t), "claude", "profile/nope", `slot profile "profile/nope" is not defined for agent "claude"`},
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
			a := slotShowApp(t)
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
	srv := slotShowServer(t)
	newApp := func() *app {
		a := pointAtFake(t, srv, "")
		a.profiles = profiles.Store{Enabled: true}
		a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
		return a
	}

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
	if n := srv.count("POST"); n != 0 {
		t.Errorf("the profile commands asked the model server to start or register (%d POSTs): %v", n, srv.reqs())
	}
}

// TestProfileCommandsReportModelOverrides follows config_content.modelOverrides
// through the real drivers into `wt profile list`, `wt profile show` and a
// launch's warnings (#412, #419): a modelOverrides that is not a table, or
// one with a target that is not a string (#418), blocks the profile, listed
// with the reason and shown blocked; a table whose targets
// are not the profile's slot models is listed ok with one warning line per
// target, naming the profile, the key and the target, and shown as a launch
// row with the same lines, each once, right under its slot lines, its
// overrides in the settings. A table whose targets are all slot models has no
// warning, in either command, and a blocked profile is shown with its reason
// alone even when its overrides would warn. Without the block, a string
// modelOverrides reached Claude Code's settings, which take an object, and a
// number target launched in the --settings as a mere warning; without
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

[[profile]]
name = "number-target"
agent = "claude"
default = "opus"
[profile.slots]
opus = "openrouter/a"
[profile.config_content.modelOverrides]
claude-sonnet-4-6 = 5
`
	store, err := profiles.ParseSlots([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	srv := slotShowServer(t)
	newApp := func() *app {
		a := pointAtFake(t, srv, "")
		a.profiles = profiles.Store{Enabled: true}
		a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
		return a
	}
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
		"  claude: profile/number-target error: config_content.modelOverrides[\"claude-sonnet-4-6\"] must be a model id string\n" +
		"  warning: " + warnings[0] + "\n" +
		"  warning: " + warnings[1] + "\n"
	if err != nil || out != wantList {
		t.Errorf("list = (%q, %v)\nwant %q, nil", out, err, wantList)
	}

	for _, tc := range []struct{ name, reason string }{
		{"string-overrides", "config_content.modelOverrides must be a table"},
		{"blocked-unrouted", "config_content.env must be a table"},
		{"number-target", `config_content.modelOverrides["claude-sonnet-4-6"] must be a model id string`},
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
	if got := slotWarningsRemote(newApp()); !slices.Equal(got, want) {
		t.Errorf("slotWarningsRemote() = %q\nwant %q", got, want)
	}
	if n := srv.count("POST"); n != 0 {
		t.Errorf("the profile commands asked the model server to start or register (%d POSTs): %v", n, srv.reqs())
	}
}

// TestProfileShowMasksTheHeaderToken verifies `wt profile show` of a profile
// with a subscription slot: the preview is the header-mode command (no
// ANTHROPIC_AUTH_TOKEN line, the stray credentials unset) and names the
// header wt adds as `header: x-wt-model-token: ` followed by the masked
// token, so the user sees what is sent without the token itself appearing
// anywhere in the output.
func TestProfileShowMasksTheHeaderToken(t *testing.T) {
	const token = "wt-SENTINEL-tok"
	rows := []modelapi.Row{
		{ID: "claude-opus-5-5", GatewayID: "claude-opus-5-5", ProviderID: config.SubscriptionProvider, ModelName: "claude-opus-5-5",
			Family: "claude", Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning,
			Action: modelapi.ActionLaunch, Order: 1, Context: 1000000},
		{ID: "openrouter/b", GatewayID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Location: "cloud",
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 2},
	}
	check := slotFixtureCheck(0, "sub", "opus", map[string]string{"opus": "claude-opus-5-5", "haiku": "openrouter/b"})
	f := newFakeModelServer(t, rows...)
	f.Token = token
	a := pointAtFake(t, f, "")
	a.profiles = profiles.Store{Enabled: true}
	a.slotChecks = []profiles.SlotCheck{check}
	out, err := runProfileCmd(t, a, "show", "-A", "claude", "-M", "profile/sub")
	if err != nil {
		t.Fatalf("err = %v; stdout =\n%s", err, out)
	}
	// The writer itself masks: handed the raw token, it still never prints
	// it (a caller that forgot to mask would otherwise leak it).
	var direct bytes.Buffer
	row := catalog.ResolveSlotProfileWire(catalog.WireByID(rows), check)
	if err := writeSlotProfileShow(&direct, "claude", "profile/sub", row, "http://127.0.0.1:1", token); err != nil {
		t.Fatalf("writeSlotProfileShow: %v", err)
	}
	for name, out := range map[string]string{"command": out, "raw-token writer": direct.String()} {
		for _, want := range []string{
			"header: x-wt-model-token: " + config.MaskKey(token) + "\n",
			"unset: ANTHROPIC_AUTH_TOKEN\n",
			"unset: CLAUDE_CODE_OAUTH_TOKEN\n",
			"env: ANTHROPIC_API_KEY=\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: stdout lacks %q; stdout =\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "env: ANTHROPIC_AUTH_TOKEN") {
			t.Errorf("%s: a header-mode preview sets ANTHROPIC_AUTH_TOKEN", name)
		}
		if strings.Contains(out, token) {
			t.Errorf("%s: the token is printed", name)
		}
	}
}

// TestProfileShowSlotProfileContext pins #689 for `wt profile show` and the
// list: the context line gives the smallest window over the slot models ("-"
// when one is unknown), the declared value and where it comes from (the
// profile's own key, else the slots), and the list names a declared key.
func TestProfileShowSlotProfileContext(t *testing.T) {
	f := newFakeModelServer(t,
		modelapi.Row{ID: "openrouter/a", GatewayID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Location: "cloud", Context: 262144,
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 1},
		modelapi.Row{ID: "openrouter/b", GatewayID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Location: "cloud", Context: 131072,
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 2},
		modelapi.Row{ID: "openrouter/c", GatewayID: "openrouter/c", ProviderID: "openrouter", ModelName: "c", Location: "cloud",
			Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 3})
	f.Token = "sk-test-1234"
	mk := func(name string, ctx int, slots map[string]string) profiles.SlotCheck {
		c := slotFixtureCheck(0, name, "sonnet", slots)
		c.Profile.Context = ctx
		return c
	}
	for _, tc := range []struct {
		name  string
		ctx   int
		slots map[string]string
		want  string
	}{
		{"derived", 0, map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"}, "context: 131k (declared 131k, from the slots)\n"},
		{"key wins", 90000, map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"}, "context: 90k (set by the profile context key; the slot models allow 131k)\n"},
		{"unknown", 0, map[string]string{"sonnet": "openrouter/c"}, "context: - (nothing declared: window unknown)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := pointAtFake(t, f, "")
			a.profiles = profiles.Store{Enabled: true}
			a.slotChecks = []profiles.SlotCheck{mk(tc.name, tc.ctx, tc.slots)}
			out, err := runProfileCmd(t, a, "show", "-A", "claude", "-M", "profile/"+tc.name)
			if err != nil || !strings.Contains(out, tc.want) {
				t.Errorf("err = %v; stdout lacks %q:\n%s", err, tc.want, out)
			}
			var sb strings.Builder
			if err := writeSlotProfileList(&sb, a); err != nil {
				t.Fatal(err)
			}
			wantList := "ok\n"
			if tc.ctx > 0 {
				wantList = "ok, context 90k\n"
			}
			if !strings.Contains(sb.String(), wantList) {
				t.Errorf("list = %q, want %q", sb.String(), wantList)
			}
		})
	}
}
