package profiles

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// slotTestDrivers stands in for the registered agent drivers. It knows three
// agents:
//   - "claude": four slots, the env and config_file mechanisms, and Claude's
//     reserved keys;
//   - "wrapped": two slots, the env, args and wrapper mechanisms, where env
//     has no effect without wrapper;
//   - "lenient": one slot, the env and config_file mechanisms, and no rule
//     of its own for config_content;
//   - "plain": a known agent with no slot support.
//
// "claude" alone has a config_content rule, the real driver's: an env key,
// when present, is a table.
func slotTestDrivers() SlotDrivers {
	slots := map[string][]string{
		"claude":  {"fable", "opus", "sonnet", "haiku"},
		"wrapped": {"big", "small"},
		"lenient": {"only"},
	}
	reserved := map[string][]string{
		"claude": {
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
			"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
			"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		},
		"wrapped": {"WRAPPED_MODEL"},
	}
	mechanisms := map[string][]Mechanism{
		"claude":  {MechanismEnv, MechanismConfigFile},
		"wrapped": {MechanismEnv, MechanismArgs, MechanismWrapper},
		"lenient": {MechanismEnv, MechanismConfigFile},
	}
	return SlotDrivers{
		Known: func(agent string) bool {
			return slices.Contains([]string{"claude", "wrapped", "lenient", "plain"}, agent)
		},
		Slots:       func(agent string) []string { return slots[agent] },
		ReservedEnv: func(agent string) []string { return reserved[agent] },
		Mechanisms:  func(agent string) []Mechanism { return mechanisms[agent] },
		Required: func(agent string, m Mechanism) (Mechanism, bool) {
			if agent == "wrapped" && m == MechanismEnv {
				return MechanismWrapper, true
			}
			return "", false
		},
		ConfigContent: func(agent string, cc map[string]any) error {
			if agent != "claude" {
				return nil
			}
			if raw, ok := cc["env"]; ok {
				if _, ok := raw.(map[string]any); !ok {
					return errors.New("config_content.env must be a table")
				}
			}
			return nil
		},
	}
}

// validLenientSlot is a profile for the "lenient" agent, whose driver has no
// rule for config_content, that passes every static rule.
func validLenientSlot() SlotProfile {
	return SlotProfile{
		Name:    "lenient-one",
		Agent:   "lenient",
		Default: "only",
		Slots:   map[string]string{"only": "ollama/mid:cloud"},
	}
}

// validClaudeSlot is a claude profile that passes every static rule.
func validClaudeSlot() SlotProfile {
	return SlotProfile{
		Name:    "mixed",
		Agent:   "claude",
		Default: "sonnet",
		Slots:   map[string]string{"opus": "openrouter/vendor/big", "sonnet": "ollama/mid:cloud"},
	}
}

// validWrappedSlot is a profile for the "wrapped" agent that passes every
// static rule while using env, args and wrapper.
func validWrappedSlot() SlotProfile {
	return SlotProfile{
		Name:    "wrapped-one",
		Agent:   "wrapped",
		Default: "big",
		Slots:   map[string]string{"big": "ollama/mid:cloud"},
		Env:     map[string]string{"WRAPPER_MODE": "accept-all"},
		Args:    []string{"--flag"},
		Wrapper: &WrapperSpec{Binary: "wrapper-bin", ArgsTemplate: []string{"--inner", "{{args}}"}},
	}
}

// TestValidateSlotsRules has one case per static rule and pins each message
// word for word, since the message is what the blocked row and `wt profile
// list` show. If a rule regresses, a profile wt cannot launch is listed as
// ok and fails later, or a sound profile is blocked.
func TestValidateSlotsRules(t *testing.T) {
	tests := []struct {
		name       string
		base       func() SlotProfile // nil: validClaudeSlot
		edit       func(p *SlotProfile)
		wantErr    string // "": the profile passes
		wantUsable bool
	}{
		// Passing profiles.
		{name: "two of four slots filled", edit: func(p *SlotProfile) {}, wantUsable: true},
		{name: "every slot filled", edit: func(p *SlotProfile) {
			p.Slots = map[string]string{"fable": "a/1", "opus": "a/2", "sonnet": "a/3", "haiku": "a/4"}
		}, wantUsable: true},
		{name: "a name of digits and hyphens", edit: func(p *SlotProfile) { p.Name = "9-lives-2" }, wantUsable: true},
		{name: "env and config_content on an agent that allows both", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
			p.ConfigContent = map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
				"env":            map[string]any{"CLAUDE_CODE_SUBAGENT_MODEL": "ollama/mid:cloud"},
			}
		}, wantUsable: true},
		{name: "env, args and wrapper on an agent that allows them", base: validWrappedSlot, edit: func(p *SlotProfile) {}, wantUsable: true},
		{name: "a config_content.env that is not a table, on an agent with no rule for it", base: validLenientSlot, edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": "x"}
		}, wantUsable: true},
		{name: "a config_content with no env key on an agent with a rule for env", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"modelOverrides": map[string]any{"a": "b"}}
		}, wantUsable: true},

		// The agent's own config_content rule.
		{name: "a config_content.env that is a string", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": "x"}
		}, wantErr: `config_content.env must be a table`, wantUsable: true},
		{name: "a config_content.env that is an array", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": []any{"ANTHROPIC_BASE_URL"}}
		}, wantErr: `config_content.env must be a table`, wantUsable: true},
		{name: "a reserved env key is reported before the agent's config_content rule", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "http://elsewhere.invalid"}
			p.ConfigContent = map[string]any{"env": "x"}
		}, wantErr: `env sets ANTHROPIC_BASE_URL, which wt sets for the launch`, wantUsable: true},

		// Rule 1.
		{name: "name missing", edit: func(p *SlotProfile) { p.Name = "" }, wantErr: `name is missing`},
		// Rule 2.
		{name: "name with an upper-case letter", edit: func(p *SlotProfile) { p.Name = "Mixed" },
			wantErr: `name "Mixed" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with an underscore", edit: func(p *SlotProfile) { p.Name = "my_profile" },
			wantErr: `name "my_profile" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with a space", edit: func(p *SlotProfile) { p.Name = "my profile" },
			wantErr: `name "my profile" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with a slash", edit: func(p *SlotProfile) { p.Name = "profile/mixed" },
			wantErr: `name "profile/mixed" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name starting with a hyphen", edit: func(p *SlotProfile) { p.Name = "-x" },
			wantErr: `name "-x" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name ending with a newline", edit: func(p *SlotProfile) { p.Name = "mixed\n" },
			wantErr: `name "mixed\n" is malformed: use lowercase letters, digits and hyphens`},
		// Rule 3.
		{name: "agent missing", edit: func(p *SlotProfile) { p.Agent = "" }, wantErr: `agent is missing`},
		// Rule 4.
		{name: "agent unknown", edit: func(p *SlotProfile) { p.Agent = "cluade" }, wantErr: `agent "cluade" is unknown`},
		// Rule 5.
		{name: "agent without slot support", edit: func(p *SlotProfile) { p.Agent = "plain" },
			wantErr: `agent "plain" does not support slot profiles`},
		// Rule 7.
		{name: "no slots table", edit: func(p *SlotProfile) { p.Slots = nil },
			wantErr: `slots is empty: assign at least one slot`, wantUsable: true},
		{name: "an empty slots table", edit: func(p *SlotProfile) { p.Slots = map[string]string{} },
			wantErr: `slots is empty: assign at least one slot`, wantUsable: true},
		// Rule 8.
		{name: "an undeclared slot", edit: func(p *SlotProfile) { p.Slots["turbo"] = "a/1" },
			wantErr: `slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "two undeclared slots report the first in sorted order", edit: func(p *SlotProfile) {
			p.Slots["turbo"] = "a/1"
			p.Slots["extra"] = "a/2"
		}, wantErr: `slot "extra" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "a slot name in the wrong case", edit: func(p *SlotProfile) { p.Slots["Haiku"] = "a/1" },
			wantErr: `slot "Haiku" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		// Rule 9.
		{name: "a slot with an empty id", edit: func(p *SlotProfile) { p.Slots["haiku"] = "" },
			wantErr: `slot "haiku" names no model`, wantUsable: true},
		// Rule 10.
		{name: "default missing", edit: func(p *SlotProfile) { p.Default = "" },
			wantErr: `default is missing`, wantUsable: true},
		// Rule 11.
		{name: "default names a declared slot the profile leaves unfilled", edit: func(p *SlotProfile) { p.Default = "haiku" },
			wantErr: `default "haiku" is not a slot this profile fills`, wantUsable: true},
		{name: "default names no slot at all", edit: func(p *SlotProfile) { p.Default = "turbo" },
			wantErr: `default "turbo" is not a slot this profile fills`, wantUsable: true},
		// Rule 12.
		{name: "args on an agent that does not allow them", edit: func(p *SlotProfile) { p.Args = []string{"--flag"} },
			wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "wrapper on an agent that does not allow it", edit: func(p *SlotProfile) {
			p.Wrapper = &WrapperSpec{Binary: "w", ArgsTemplate: []string{"{{args}}"}}
		}, wantErr: `agent "claude" does not support mechanism "wrapper"`, wantUsable: true},
		{name: "args and wrapper both refused report args first", edit: func(p *SlotProfile) {
			p.Args = []string{"--flag"}
			p.Wrapper = &WrapperSpec{Binary: "w", ArgsTemplate: []string{"{{args}}"}}
		}, wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "config_content on an agent that does not allow it", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"k": "v"}
		}, wantErr: `agent "wrapped" does not support mechanism "config_file"`, wantUsable: true},
		// Rule 13.
		{name: "a mechanism without its required partner", base: validWrappedSlot, edit: func(p *SlotProfile) { p.Wrapper = nil },
			wantErr: `mechanism "env" has no effect without "wrapper" on the same profile`, wantUsable: true},
		// Rule 14.
		{name: "env sets a reserved key", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"MAX_THINKING_TOKENS": "4096", "ANTHROPIC_BASE_URL": "http://elsewhere"}
		}, wantErr: `env sets ANTHROPIC_BASE_URL, which wt sets for the launch`, wantUsable: true},
		{name: "env sets a slot's model variable", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5"}
		}, wantErr: `env sets ANTHROPIC_DEFAULT_HAIKU_MODEL, which wt sets for the launch`, wantUsable: true},
		{name: "two reserved env keys report the first in sorted order", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x", "ANTHROPIC_API_KEY": "y"}
		}, wantErr: `env sets ANTHROPIC_API_KEY, which wt sets for the launch`, wantUsable: true},
		// Rule 15.
		{name: "config_content.env sets a reserved key", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": map[string]any{"OTHER": "1", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "x"}}
		}, wantErr: `config_content.env sets ANTHROPIC_DEFAULT_OPUS_MODEL_NAME, which wt sets for the launch`, wantUsable: true},
		// Rule 16.
		{name: "wrapper without the args placeholder", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.Wrapper.ArgsTemplate = []string{"--inner"}
		}, wantErr: `wrapper args_template [--inner] has no "{{args}}" placeholder`, wantUsable: true},
		{name: "wrapper with the placeholder only inside another element", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.Wrapper.ArgsTemplate = []string{"--inner={{args}}"}
		}, wantErr: `wrapper args_template [--inner={{args}}] has no "{{args}}" placeholder`, wantUsable: true},

		// The first failing rule is the one reported.
		{name: "a missing name is reported before a missing agent", edit: func(p *SlotProfile) { p.Name, p.Agent = "", "" },
			wantErr: `name is missing`},
		{name: "an unknown agent is reported before empty slots", edit: func(p *SlotProfile) { p.Agent, p.Slots = "cluade", nil },
			wantErr: `agent "cluade" is unknown`},
		{name: "an undeclared slot is reported before a missing default", edit: func(p *SlotProfile) {
			p.Slots["turbo"] = "a/1"
			p.Default = ""
		}, wantErr: `slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "an unfilled default is reported before a refused mechanism", edit: func(p *SlotProfile) {
			p.Default = "haiku"
			p.Args = []string{"--flag"}
		}, wantErr: `default "haiku" is not a slot this profile fills`, wantUsable: true},
		{name: "a refused mechanism is reported before a reserved env key", edit: func(p *SlotProfile) {
			p.Args = []string{"--flag"}
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x"}
		}, wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "a reserved env key is reported before one under config_content.env", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x"}
			p.ConfigContent = map[string]any{"env": map[string]any{"ANTHROPIC_API_KEY": "y"}}
		}, wantErr: `env sets ANTHROPIC_BASE_URL, which wt sets for the launch`, wantUsable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := validClaudeSlot
			if tc.base != nil {
				base = tc.base
			}
			p := base()
			tc.edit(&p)
			checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{p}}, slotTestDrivers())
			if len(checks) != 1 {
				t.Fatalf("ValidateSlots() gave %d checks, want 1", len(checks))
			}
			c := checks[0]
			got := ""
			if c.Err != nil {
				got = c.Err.Error()
			}
			if got != tc.wantErr {
				t.Errorf("Err = %q, want %q", got, tc.wantErr)
			}
			if c.Usable != tc.wantUsable {
				t.Errorf("Usable = %v, want %v", c.Usable, tc.wantUsable)
			}
			if c.Duplicate {
				t.Errorf("Duplicate = true for a file with one profile")
			}
			wantDeclared := []string(nil)
			if tc.wantUsable {
				wantDeclared = slotTestDrivers().Slots(p.Agent)
			}
			if !reflect.DeepEqual(c.Declared, wantDeclared) {
				t.Errorf("Declared = %v, want %v", c.Declared, wantDeclared)
			}
			if c.Index != 0 || !reflect.DeepEqual(c.Profile, p) {
				t.Errorf("check carries Index %d and Profile %#v, want 0 and the profile as given", c.Index, c.Profile)
			}
		})
	}
}

// TestValidateSlotsDuplicateNames checks rule 6: profiles that share a name
// for one agent are all marked duplicate, and nothing else is. If it
// regresses, two rows get the same id `profile/<name>` and a pin launches
// whichever comes first, or a profile is blocked for a name another agent
// or a broken entry happens to use.
func TestValidateSlotsDuplicateNames(t *testing.T) {
	first := validClaudeSlot()
	second := validClaudeSlot()
	second.Default = "" // would fail rule 10 if the duplicate rule did not come first
	otherAgent := validWrappedSlot()
	otherAgent.Name = "mixed"
	unknownAgent := validClaudeSlot()
	unknownAgent.Agent = "cluade"
	alone := validClaudeSlot()
	alone.Name = "alone"

	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{first, otherAgent, unknownAgent, second, alone}}, slotTestDrivers())
	if len(checks) != 5 {
		t.Fatalf("ValidateSlots() gave %d checks, want 5", len(checks))
	}
	for _, i := range []int{0, 3} {
		c := checks[i]
		if !c.Usable || !c.Duplicate || !errors.Is(c.Err, ErrSlotDuplicate) {
			t.Errorf("checks[%d]: Usable=%v Duplicate=%v Err=%v, want a usable duplicate with ErrSlotDuplicate", i, c.Usable, c.Duplicate, c.Err)
		}
		if c.Err == nil || c.Err.Error() != "duplicate profile name" {
			t.Errorf("checks[%d].Err = %v, want %q", i, c.Err, "duplicate profile name")
		}
		if !reflect.DeepEqual(c.Declared, []string{"fable", "opus", "sonnet", "haiku"}) {
			t.Errorf("checks[%d].Declared = %v, want claude's slots", i, c.Declared)
		}
	}
	if c := checks[1]; !c.Usable || c.Duplicate || c.Err != nil {
		t.Errorf("same name under another agent: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Usable, c.Duplicate, c.Err)
	}
	if c := checks[2]; c.Usable || c.Duplicate || c.Err == nil || c.Err.Error() != `agent "cluade" is unknown` {
		t.Errorf("same name under an unknown agent: Usable=%v Duplicate=%v Err=%v, want unusable with its own error", c.Usable, c.Duplicate, c.Err)
	}
	if c := checks[4]; !c.Usable || c.Duplicate || c.Err != nil {
		t.Errorf("a profile with its own name: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Usable, c.Duplicate, c.Err)
	}
}

// TestValidateSlotsJudgesEachProfileAlone checks that one check comes back
// per profile, in file order, and that a broken profile changes no other
// profile's verdict. If it regresses, one typo blocks every profile in the
// file, or a verdict is attached to the wrong row.
func TestValidateSlotsJudgesEachProfileAlone(t *testing.T) {
	noName := validClaudeSlot()
	noName.Name = ""
	badSlot := validClaudeSlot()
	badSlot.Name = "bad-slot"
	badSlot.Slots = map[string]string{"turbo": "a/1"}
	good := validClaudeSlot()
	wrapped := validWrappedSlot()

	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{noName, badSlot, good, wrapped}}, slotTestDrivers())
	if len(checks) != 4 {
		t.Fatalf("ValidateSlots() gave %d checks, want 4", len(checks))
	}
	wantLabels := []string{"slot-profiles.toml[0]", "profile/bad-slot", "profile/mixed", "profile/wrapped-one"}
	wantOK := []bool{false, false, true, true}
	for i, c := range checks {
		if c.Index != i {
			t.Errorf("checks[%d].Index = %d, want %d", i, c.Index, i)
		}
		if c.Label() != wantLabels[i] {
			t.Errorf("checks[%d].Label() = %q, want %q", i, c.Label(), wantLabels[i])
		}
		if (c.Err == nil) != wantOK[i] {
			t.Errorf("checks[%d].Err = %v, want passing = %v", i, c.Err, wantOK[i])
		}
	}
	if got := ValidateSlots(SlotStore{}, slotTestDrivers()); len(got) != 0 {
		t.Errorf("ValidateSlots() on an empty store = %v, want no checks", got)
	}
}

// TestSlotCheckLabel checks how a profile is named in a warning line and in
// `wt profile list`: by its row id when it has one, else by its position in
// the file. If it regresses, a profile with a malformed name is reported
// under an id that no row and no pin can ever have.
func TestSlotCheckLabel(t *testing.T) {
	tests := []struct {
		name  string
		check SlotCheck
		want  string
	}{
		{name: "usable", check: SlotCheck{Index: 4, Usable: true, Profile: SlotProfile{Name: "mixed"}}, want: "profile/mixed"},
		{name: "unusable with a malformed name", check: SlotCheck{Index: 4, Profile: SlotProfile{Name: "Mixed"}}, want: "slot-profiles.toml[4]"},
		{name: "unusable with no name", check: SlotCheck{Index: 0}, want: "slot-profiles.toml[0]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.check.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateSlotsWithoutRequiredCallback checks that SlotDrivers.Required
// may be nil. If it regresses, a caller that has no partner rule to report
// crashes wt while it builds the picker's rows.
func TestValidateSlotsWithoutRequiredCallback(t *testing.T) {
	d := slotTestDrivers()
	d.Required = nil
	p := validWrappedSlot()
	p.Wrapper = nil // rule 13 would fail this with the callback set
	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{p}}, d)
	if len(checks) != 1 || checks[0].Err != nil {
		t.Errorf("ValidateSlots() with a nil Required = %+v, want one passing check", checks)
	}
}

// TestValidateSlotsDeclaredIsACopy checks that a check's Declared list is
// its own. If it regresses, a caller that sorts or trims one profile's list
// reorders the slots of every other profile and of the driver itself.
func TestValidateSlotsDeclaredIsACopy(t *testing.T) {
	shared := []string{"fable", "opus", "sonnet", "haiku"}
	d := slotTestDrivers()
	d.Slots = func(agent string) []string { return shared }
	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{validClaudeSlot()}}, d)
	checks[0].Declared[0] = "changed"
	if shared[0] != "fable" {
		t.Errorf("writing to Declared changed the driver's own slot list to %v", shared)
	}
}

// TestValidateSlotsWithoutConfigContentCallback checks what a SlotDrivers
// with no ConfigContent callback does: a profile that sets config_content is
// refused, since nothing can say whether its agent accepts the content, and a
// profile without config_content passes as before. If the missing callback
// passed the content instead, a caller that forgot to wire it would show a
// launch row for a profile whose launch the slot build then refuses.
func TestValidateSlotsWithoutConfigContentCallback(t *testing.T) {
	d := slotTestDrivers()
	d.ConfigContent = nil
	with := validClaudeSlot()
	with.ConfigContent = map[string]any{"modelOverrides": map[string]any{"a": "b"}}
	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{with}}, d)
	const want = `config_content cannot be checked for agent "claude"`
	if len(checks) != 1 || checks[0].Err == nil || checks[0].Err.Error() != want {
		t.Errorf("ValidateSlots() with a nil ConfigContent = %+v, want one check with Err %q", checks, want)
	}
	checks = ValidateSlots(SlotStore{Profiles: []SlotProfile{validClaudeSlot()}}, d)
	if len(checks) != 1 || checks[0].Err != nil {
		t.Errorf("ValidateSlots() of a profile with no config_content = %+v, want one passing check", checks)
	}
}
