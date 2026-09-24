package profiles

import "testing"

// TestValidateRejectsUnsupportedMechanism verifies a profile using a
// mechanism its agent does not declare (e.g. config_content for an agent
// that only declares wrapper) fails loudly at validation time, naming the
// offending profile — so a bad hand-edit to profiles.toml is caught at
// startup, not silently misapplied (or silently dropped) at launch time.
func TestValidateRejectsUnsupportedMechanism(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "pi", Match: "location", Location: "local", ConfigContent: map[string]any{"x": "1"}},
	}}
	mechs := func(agent string) []Mechanism {
		if agent == "pi" {
			return []Mechanism{MechanismWrapper}
		}
		return nil
	}
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the config_content/pi mismatch")
	}
}

// TestValidateAcceptsDeclaredMechanisms verifies a profile using only
// mechanisms its agent declares passes cleanly, so a correctly authored
// profiles.toml never gets a spurious startup warning.
func TestValidateAcceptsDeclaredMechanisms(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismEnv, MechanismConfigFile} }
	if err := Validate(store, mechs); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestValidateUnknownAgentRejectsAnyMechanism verifies a profile naming an
// agent with no declared mechanisms at all (e.g. copilot, or a typo)
// fails the same way an explicitly-unsupported mechanism does.
func TestValidateUnknownAgentRejectsAnyMechanism(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "copilot", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	mechs := func(agent string) []Mechanism { return nil }
	if err := Validate(store, mechs); err == nil {
		t.Fatal("Validate() = nil, want an error for copilot (no declared mechanisms)")
	}
}
