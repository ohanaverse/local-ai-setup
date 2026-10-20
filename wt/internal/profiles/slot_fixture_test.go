package profiles

import (
	"os"
	"reflect"
	"testing"
)

// TestSlotProfilesFixtureMatchesTheParser reads the contract fixture
// docs/contracts/slot-profiles.sample.toml, the documented shape of
// ~/.config/agent-wt/slot-profiles.toml. It pins every field the fixture
// sets and checks that every profile, opusplan's included, passes static validation for an agent
// with Claude's slots, mechanisms and reserved keys. If the parser and the
// documented format drift apart, a file written from the documentation
// loads with a field missing or as a blocked row; this test fails first.
func TestSlotProfilesFixtureMatchesTheParser(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/slot-profiles.sample.toml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	store, err := ParseSlots(data)
	if err != nil {
		t.Fatalf("ParseSlots(fixture) error = %v, want nil", err)
	}
	want := []SlotProfile{
		{
			Name:    "mixed",
			Agent:   "claude",
			Default: "sonnet",
			Slots: map[string]string{
				"opus":   "openrouter/z-ai/glm-5.3-flash",
				"sonnet": "ollama/glm-5.3-flash:cloud",
				"haiku":  "omlx/mlx-community--Qwen3.8-27B-4bit",
			},
			Env: map[string]string{"MAX_THINKING_TOKENS": "4096"},
			ConfigContent: map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"},
			},
		},
		{
			Name:    "one-slot",
			Agent:   "claude",
			Default: "opus",
			Slots:   map[string]string{"opus": "ollama/glm-5.2:cloud"},
		},
		{
			Name:    "planned",
			Agent:   "claude",
			Default: "opusplan",
			Slots: map[string]string{
				"opus":   "openrouter/z-ai/glm-5.3",
				"sonnet": "ollama/glm-5.3-flash:cloud",
			},
		},
	}
	if !reflect.DeepEqual(store.Profiles, want) {
		t.Fatalf("fixture profiles =\n%#v\nwant\n%#v", store.Profiles, want)
	}

	checks := ValidateSlots(store, slotTestDrivers())
	if len(checks) != len(want) {
		t.Fatalf("ValidateSlots(fixture) gave %d checks, want %d", len(checks), len(want))
	}
	for _, c := range checks {
		if !c.Usable || c.Duplicate || c.Err != nil {
			t.Errorf("%s: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Label(), c.Usable, c.Duplicate, c.Err)
		}
	}
	if got, want := checks[0].FilledOrder(), []string{"sonnet", "opus", "haiku"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mixed: FilledOrder() = %v, want %v (the default, then the declared order)", got, want)
	}
	if got, want := checks[1].Label(), "profile/one-slot"; got != want {
		t.Errorf("checks[1].Label() = %q, want %q", got, want)
	}
	if got := checks[2].RestSlot(); got != "sonnet" {
		t.Errorf("planned: RestSlot() = %q, want sonnet (opusplan works on the sonnet slot)", got)
	}
}
