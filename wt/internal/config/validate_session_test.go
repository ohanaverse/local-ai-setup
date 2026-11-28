package config

import "testing"

// TestValidateSessionChecksOnlyWhatConfigTomlOwns proves the session-side
// validation reports, in order, the default_tag and agent problems a
// session-side save can repair (an empty or repeated name), and skips the
// agents LoadSession synthesized. Why: a session reads no registry and an
// agent names no provider, so a name is all that can be wrong with an entry.
func TestValidateSessionChecksOnlyWhatConfigTomlOwns(t *testing.T) {
	ok := &Config{DefaultTag: "code", Agents: []Agent{{Name: "claude"}}}
	if err := ok.ValidateSession(); err != nil {
		t.Fatalf("a named agent was refused: %v", err)
	}
	bad := &Config{Agents: []Agent{
		{Name: "a"}, {Name: "a"},
		{Name: "b"}, {Name: "c"},
		{Name: ""},
		{Name: "synth", Synthesized: true}}}
	got := bad.ValidateSession().Error()
	want := "default_tag must not be empty\nduplicate agent name \"a\"\nagent entry with empty name"
	if got != want {
		t.Fatalf("ValidateSession = %q\nwant %q", got, want)
	}
}

// TestValidateAcceptsAnAgentWithNoList proves an agent entry that names no
// provider is valid on both sides, the session's ValidateSession and the
// registry's ValidateAll. Why: the eligibility rule derives a provider's
// agents from the provider and the model, so a list is no longer required, and
// requiring one would reject the entries every new install writes.
func TestValidateAcceptsAnAgentWithNoList(t *testing.T) {
	c := &Config{DefaultTag: "code", Agents: []Agent{{Name: "x"}}}
	if err := c.ValidateSession(); err != nil {
		t.Errorf("ValidateSession refused an agent with no list: %v", err)
	}
	c.Providers = []Provider{{ID: "ollama"}}
	if err := c.ValidateAll(); err != nil {
		t.Errorf("ValidateAll refused an agent with no list: %v", err)
	}
}

// TestValidateAllKeepsItsAgentErrors proves the shared agent loop still names
// an empty and a repeated agent name for the registry-side ValidateAll, so the
// extraction changed nothing on the host side, and that a synthesized agent
// is skipped there too. Why: with no provider check left the name checks are
// all the loop does, and both callers must keep them.
func TestValidateAllKeepsItsAgentErrors(t *testing.T) {
	c := &Config{DefaultTag: "code", Agents: []Agent{{Name: "a"}, {Name: "a"}, {Name: ""}, {Name: "synth", Synthesized: true}}}
	err := c.ValidateAll()
	want := "duplicate agent name \"a\"\nagent entry with empty name"
	if err == nil || err.Error() != want {
		t.Fatalf("ValidateAll = %v, want %q", err, want)
	}
}
