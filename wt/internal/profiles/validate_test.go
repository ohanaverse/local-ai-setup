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

// TestValidateMultipleOffendingProfiles verifies that Validate collects
// and reports every offending profile in one combined error message — so
// a hand-edited profiles.toml with multiple violations is surfaced all at
// once, not one at a time, and a user can fix them all in one edit cycle.
func TestValidateMultipleOffendingProfiles(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", ConfigContent: map[string]any{"x": "1"}},
		{Agent: "pi", Match: "location", Location: "local", Args: []string{"--arg"}},
	}}
	mechs := func(agent string) []Mechanism {
		if agent == "claude" {
			return []Mechanism{MechanismEnv}
		}
		if agent == "pi" {
			return []Mechanism{MechanismWrapper}
		}
		return nil
	}
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming both offending profiles")
	}
	errMsg := err.Error()
	// Both profiles should be mentioned by index in the combined error message
	if !contains(errMsg, "profiles.toml[0]") || !contains(errMsg, "profiles.toml[1]") {
		t.Errorf("Validate() error message does not name both profiles: %v", errMsg)
	}
}

// TestValidateZeroMechanismsAlwaysPasses verifies that a profile using no
// mechanisms at all (no Env, Args, ConfigContent, or Wrapper) always passes
// validation regardless of what mechanisms its agent declares or doesn't
// declare — a bare profile with just agent/match/selector fields has nothing
// to validate and is always valid.
func TestValidateZeroMechanismsAlwaysPasses(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "unknown", Match: "location", Location: "local"},
	}}
	mechs := func(agent string) []Mechanism { return nil }
	if err := Validate(store, mechs); err != nil {
		t.Errorf("Validate() = %v, want nil for profile with zero mechanisms", err)
	}
}

// TestValidateRejectsInvalidMatchTier verifies a profile whose `match`
// field is anything other than "location"/"provider"/"model" (typically a
// typo) fails validation loudly instead of passing silently — before this
// check existed, Resolve's tierRank lookup silently skipped such a profile
// forever, with no error anywhere to tell the user why it never applied.
func TestValidateRejectsInvalidMatchTier(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "locaton", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismEnv} }
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the invalid match tier")
	}
	if !contains(err.Error(), "invalid match") {
		t.Errorf("Validate() error = %v, want it to mention the invalid match value", err)
	}
}

// TestValidateRejectsEmptyMatchField is the regression lock for the other
// half of the matchesTier empty-field bug: a profile whose match tier
// names a field left empty (e.g. `match = "model"` with no `model = ...`)
// must fail loudly at validation time — the same way an invalid match tier
// value already does — rather than silently becoming a wildcard that
// matches every launch of its agent.
func TestValidateRejectsEmptyMatchField(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "model", Env: map[string]string{"X": "1"}}, // Model left empty
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismEnv} }
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the empty model match field")
	}
	if !contains(err.Error(), "empty") {
		t.Errorf("Validate() error = %v, want it to mention the empty match field", err)
	}
}

// Helper function for test assertions
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
