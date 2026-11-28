package config

import (
	"slices"
	"testing"
)

// agentWith builds a config.toml-shaped agent entry with the given list.
func agentWith(name string, list ...string) Agent {
	return Agent{Name: name, SupportedProviders: list}
}

// TestIgnoredAgentListsStaysSilentForStockShapes pins the false-positive
// guard of the one-note-per-process notice: every list wt itself ever wrote
// (the seed's [name, ollama], the sushi-extended [name, ollama, sushi], agy's
// [agy], pi's and opencode's [ollama] and [ollama, sushi]) narrows nothing
// the derived eligibility rule would not offer, so it must not be noted.
// The note runs on every start of every machine, so a stock shape that
// fired would be a permanent nag nobody can silence short of editing the
// file. Order does not matter to the judge: the lists are compared as sets,
// because a reordering of stock providers narrows nothing either (wt itself
// only ever wrote them in the orders below, so this is the quieter reading,
// never a missed customization). An agent with no list and a Synthesized
// agent are silent too.
func TestIgnoredAgentListsStaysSilentForStockShapes(t *testing.T) {
	cases := []struct {
		name string
		a    Agent
	}{
		{"claude name+ollama", agentWith("claude", "claude", "ollama")},
		{"claude name+ollama+sushi", agentWith("claude", "claude", "ollama", "sushi")},
		{"codex name+ollama", agentWith("codex", "codex", "ollama")},
		{"copilot name+ollama+sushi", agentWith("copilot", "copilot", "ollama", "sushi")},
		{"pi name+ollama", agentWith("pi", "pi", "ollama")},
		{"pi ollama", agentWith("pi", "ollama")},
		{"pi ollama+sushi", agentWith("pi", "ollama", "sushi")},
		{"opencode ollama", agentWith("opencode", "ollama")},
		{"opencode ollama+sushi", agentWith("opencode", "ollama", "sushi")},
		{"agy agy", agentWith("agy", "agy")},
		{"agy name+ollama", agentWith("agy", "agy", "ollama")},
		// Different order: the judge is order-insensitive (see the comment above).
		{"claude ollama+claude", agentWith("claude", "ollama", "claude")},
		{"claude sushi+ollama+claude", agentWith("claude", "sushi", "ollama", "claude")},
		{"pi sushi+ollama", agentWith("pi", "sushi", "ollama")},
		{"no list", Agent{Name: "codex"}},
		{"empty list", Agent{Name: "codex", SupportedProviders: []string{}}},
		{"synthesized", Agent{Name: "codex", Synthesized: true}},
		{"synthesized with a custom list", Agent{Name: "codex", Synthesized: true, SupportedProviders: []string{"openrouter"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Agents: []Agent{tc.a}}
			if got := cfg.IgnoredAgentLists(); len(got) != 0 {
				t.Errorf("IgnoredAgentLists() = %q, want none for %v", got, tc.a.SupportedProviders)
			}
		})
	}
	// A nil Config is silent, as IgnoredLitellmKeys' is.
	var nilCfg *Config
	if got := nilCfg.IgnoredAgentLists(); len(got) != 0 {
		t.Errorf("nil Config: IgnoredAgentLists() = %q, want none", got)
	}
}

// TestIgnoredAgentListsNamesACustomizedList pins what the note is for: a
// list a person edited (an extra provider, a different one, a stock shape
// that belongs to another agent) is ignored by the derived rule, and the
// agent is named, in file order, so the person learns the edit does nothing.
// The agy-only shape is stock for agy alone, and pi's bare [ollama] only for
// pi and opencode: claude with [ollama] dropped its own name and is a
// customization.
func TestIgnoredAgentListsNamesACustomizedList(t *testing.T) {
	cfg := &Config{Agents: []Agent{
		agentWith("claude", "claude", "ollama"), // stock, silent
		agentWith("codex", "codex", "ollama", "openrouter"),
		agentWith("pi", "ollama"), // stock, silent
		agentWith("opencode", "claude", "openrouter"),
		{Name: "agy", Synthesized: true},
		agentWith("copilot", "openrouter"),
		agentWith("gemini", "agy"), // [agy] is stock for agy only
		agentWith("cursor", "ollama"),
		agentWith("claude", "claude"),          // own name only: not stock (agy alone is)
		agentWith("claude", "ollama", "sushi"), // pi's bare shape on an agent with a native provider
		agentWith("pi", "pi"),                  // no ollama
	}}
	want := []string{"codex", "opencode", "copilot", "gemini", "cursor", "claude", "claude", "pi"}
	if got := cfg.IgnoredAgentLists(); !slices.Equal(got, want) {
		t.Errorf("IgnoredAgentLists() = %q, want %q", got, want)
	}
}
