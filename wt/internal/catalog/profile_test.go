// Tests for slot-profile rows: resolved validation (the rules that need the
// registry, the live inventory and the [litellm] table), the row Build emits
// for a profile, and what keeps those rows away from callers that did not ask
// for them.
package catalog

import (
	"errors"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestProfileRowAnswersFromItsVerdict verifies that a row carrying a profile
// reports the profile's verdict and reason, not the location-and-provider
// answer a model row gives. A profile row has no location, so without this
// every profile would read as a launch row and a blocked one would start
// Claude Code with slots that cannot work.
func TestProfileRowAnswersFromItsVerdict(t *testing.T) {
	blocked := Row{Model: config.Model{ID: "profile/x"}, Profile: &ProfileRow{Verdict: ActionBlock, Reason: "slot haiku: nope"}}
	if !blocked.IsProfile() || blocked.Action() != ActionBlock || blocked.BlockReason() != "slot haiku: nope" {
		t.Errorf("blocked profile row: IsProfile=%v action=%v reason=%q", blocked.IsProfile(), blocked.Action(), blocked.BlockReason())
	}
	start := Row{Model: config.Model{ID: "profile/y"}, Profile: &ProfileRow{Verdict: ActionStart}}
	if start.Action() != ActionStart || start.BlockReason() != "" {
		t.Errorf("start profile row: action=%v reason=%q", start.Action(), start.BlockReason())
	}
	model := Row{Location: config.LocationCloud, Model: config.Model{ID: "openrouter/a"}}
	if model.IsProfile() || model.Action() != ActionLaunch {
		t.Errorf("model row: IsProfile=%v action=%v, want an ordinary launch row", model.IsProfile(), model.Action())
	}
}

// slotDeclared are the slots the test agent declares, in display order.
var slotDeclared = []string{"fable", "opus", "sonnet", "haiku"}

// slotCheck is a profile for agent "claude" that passed static validation.
func slotCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: slotDeclared,
	}
}

// TestBuildBlocksAProfileTheAgentsContentRuleRefuses follows a profile from
// static validation to its row: when the agent's driver refuses the
// profile's config_content, the profile is still Usable, so it gets a row
// (ResolveSlotProfileWire over the model server's rows), and the row is
// blocked with the driver's message. The same content for an
// agent whose driver has no such rule, and content the rule accepts, give a
// launch row. Before the rule was static, such a profile was drawn as a
// launch row and failed at the slot build, after the routes were written.
func TestBuildBlocksAProfileTheAgentsContentRuleRefuses(t *testing.T) {
	drivers := profiles.SlotDrivers{
		Known:       func(string) bool { return true },
		Slots:       func(string) []string { return slotDeclared },
		ReservedEnv: func(string) []string { return nil },
		Mechanisms: func(string) []profiles.Mechanism {
			return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile}
		},
		ConfigContent: func(agent string, cc map[string]any) error {
			if _, table := cc["env"].(map[string]any); agent == "claude" && cc["env"] != nil && !table {
				return errors.New("config_content.env must be a table")
			}
			return nil
		},
	}
	profile := func(name, agent string, env any) profiles.SlotProfile {
		return profiles.SlotProfile{Name: name, Agent: agent, Default: "sonnet",
			Slots: map[string]string{"sonnet": "openrouter/a"}, ConfigContent: map[string]any{"env": env}}
	}
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{
		profile("string-env", "claude", "x"),
		profile("table-env", "claude", map[string]any{"MAX_THINKING_TOKENS": "4096"}),
		profile("other-agent", "codex", "x"),
	}}
	checks := profiles.ValidateSlots(store, drivers)
	for _, tc := range []struct {
		agent, id  string
		wantAction Action
		wantReason string
	}{
		{"claude", "profile/string-env", ActionBlock, "config_content.env must be a table"},
		{"claude", "profile/table-env", ActionLaunch, ""},
		{"codex", "profile/other-agent", ActionLaunch, ""},
	} {
		var row Row
		found := false
		for _, c := range checks {
			if c.Usable && c.Profile.Agent == tc.agent && c.Profile.ID() == tc.id {
				row, found = ResolveSlotProfileWire(wireSlotRows(), c), true
			}
		}
		if !found {
			t.Errorf("%s: no usable profile for agent %s", tc.id, tc.agent)
			continue
		}
		if row.Action() != tc.wantAction || row.BlockReason() != tc.wantReason {
			t.Errorf("%s: action = %v, reason = %q\nwant %v, %q", tc.id, row.Action(), row.BlockReason(), tc.wantAction, tc.wantReason)
		}
	}
}
