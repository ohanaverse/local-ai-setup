package config

import "testing"

// TestValidateSessionChecksOnlyWhatConfigTomlOwns proves the session-side
// validation reports, in order, the default_tag and agent problems a
// session-side save can repair (an empty or repeated name), never refuses an
// unknown provider id (a session reads no registry), no longer asks an agent
// for a provider list or a default inside it (the eligibility rule reads
// neither), and skips the agents LoadSession synthesized.
func TestValidateSessionChecksOnlyWhatConfigTomlOwns(t *testing.T) {
	ok := &Config{DefaultTag: "code", Agents: []Agent{{Name: "claude", SupportedProviders: []string{"nosuch"}, DefaultProvider: "nosuch"}}}
	if err := ok.ValidateSession(); err != nil {
		t.Fatalf("unknown provider id refused: %v", err)
	}
	bad := &Config{Agents: []Agent{
		{Name: "a", SupportedProviders: []string{"p"}}, {Name: "a", SupportedProviders: []string{"p"}},
		{Name: "b"}, {Name: "c", SupportedProviders: []string{"p"}, DefaultProvider: "q"},
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
// an unknown provider for the registry-side ValidateAll, so the extraction
// changed nothing on the host side.
func TestValidateAllKeepsItsAgentErrors(t *testing.T) {
	c := &Config{DefaultTag: "code", Agents: []Agent{{Name: "a", SupportedProviders: []string{"nosuch"}}}}
	if err := c.ValidateAll(); err == nil || err.Error() != `agent "a": unknown provider "nosuch"` {
		t.Fatalf("ValidateAll = %v", err)
	}
}

// TestValidateAllStillRefusesAnUnknownListedProvider proves an agent that
// still lists a provider the registry does not hold fails ValidateAll with the
// same text as before the list became optional. Why: PR 1 keeps the list
// decoded and written back, and a dangling id in it is still a typo worth
// reporting on the host side; only the session side ignores it.
func TestValidateAllStillRefusesAnUnknownListedProvider(t *testing.T) {
	c := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama"}},
		Agents:     []Agent{{Name: "a", SupportedProviders: []string{"ollama", "nope"}}},
	}
	err := c.ValidateAll()
	if err == nil || err.Error() != `agent "a": unknown provider "nope"` {
		t.Fatalf("ValidateAll = %v, want the unknown provider %q error", err, "nope")
	}
	if err := c.ValidateSession(); err != nil {
		t.Errorf("ValidateSession must not read the registry: %v", err)
	}
}
