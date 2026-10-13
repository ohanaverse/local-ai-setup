package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

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
		},
	}
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
