package config

import "testing"

// TestFinalizeCfgDerivesSushiForClaudeAndPi verifies a registry holding a
// sushi model makes claude and pi support sushi in memory, whatever wrote the
// row (`wt model add` is not the only way: the Models tab, a hand edit and a
// restored registry never run it). Without the entry `wt smoke` and the
// pickers say no agent supports the model. Nothing else changes: a registry
// with no sushi model, an agent that is absent, and an agent with no
// providers at all are left exactly as they were, and the derivation never
// duplicates an entry the file already has.
func TestFinalizeCfgDerivesSushiForClaudeAndPi(t *testing.T) {
	agents := func() []Agent {
		return []Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "ollama"}},
			{Name: "pi", SupportedProviders: []string{"pi", "sushi"}},
			{Name: "codex", SupportedProviders: []string{"codex"}},
		}
	}
	sushi := []Provider{{ID: "sushi"}}
	model := []Model{{ID: "sushi/p", ProviderID: "sushi", ModelName: "p"}}

	cfg := finalizeCfg(&Config{Agents: agents()}, sushi, model)
	if !cfg.AgentSupportsProvider("claude", "sushi") {
		t.Error("claude does not support sushi with a sushi model registered")
	}
	if n := countOf(cfg.Agents[1].SupportedProviders, "sushi"); n != 1 {
		t.Errorf("pi lists sushi %d times, want once", n)
	}
	if cfg.AgentSupportsProvider("codex", "sushi") {
		t.Error("codex was given sushi; only claude and pi run it")
	}

	cfg = finalizeCfg(&Config{Agents: agents()}, sushi, nil)
	if cfg.AgentSupportsProvider("claude", "sushi") {
		t.Error("claude supports sushi with no sushi model registered")
	}
	cfg = finalizeCfg(&Config{Agents: agents()}, []Provider{{ID: "ollama"}}, model)
	if cfg.AgentSupportsProvider("claude", "sushi") {
		t.Error("sushi was listed for an agent though the registry has no sushi provider row")
	}
	cfg = finalizeCfg(&Config{Agents: []Agent{{Name: "claude"}}}, sushi, model)
	if len(cfg.Agents[0].SupportedProviders) != 0 {
		t.Errorf("an agent with no providers was given %v", cfg.Agents[0].SupportedProviders)
	}
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
