package config

import "testing"

// TestValidateSessionChecksOnlyWhatConfigTomlOwns proves the session-side
// validation reports, in order, the default_tag and agent problems a
// session-side save can repair, never refuses an unknown provider id (a
// session reads no registry) and skips the agents LoadSession synthesized.
func TestValidateSessionChecksOnlyWhatConfigTomlOwns(t *testing.T) {
	ok := &Config{DefaultTag: "code", Agents: []Agent{{Name: "claude", SupportedProviders: []string{"nosuch"}, DefaultProvider: "nosuch"}}}
	if err := ok.ValidateSession(); err != nil {
		t.Fatalf("unknown provider id refused: %v", err)
	}
	bad := &Config{Agents: []Agent{
		{Name: "a", SupportedProviders: []string{"p"}}, {Name: "a", SupportedProviders: []string{"p"}},
		{Name: "b"}, {Name: "c", SupportedProviders: []string{"p"}, DefaultProvider: "q"},
		{Name: "synth", AllProviders: true}}}
	got := bad.ValidateSession().Error()
	want := "default_tag must not be empty\nduplicate agent name \"a\"\nagent \"b\": must have at least one supported provider\nagent \"c\": default provider \"q\" not in supported_providers"
	if got != want {
		t.Fatalf("ValidateSession = %q\nwant %q", got, want)
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
