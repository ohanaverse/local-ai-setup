package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestBuildAgentList verifies the agent+command picker contains every
// configured agent and every registered command, with their kinds
// correctly labeled. This drives the `phaseAgent` view.
func TestBuildAgentList(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "codex", SupportedProviders: []string{"openai"}},
		},
	}
	items := buildAgentList(cfg, 80, 24)
	if len(items) < 3 { // 2 agents + 1 command (shell)
		t.Fatalf("expected at least 3 items, got %d", len(items))
	}
	var sawShell, sawClaude, sawCodex bool
	for _, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			t.Fatalf("item %T is not an agentItem", it)
		}
		switch ai.name {
		case "shell":
			sawShell = true
			if !ai.command {
				t.Error("shell should be marked as a command")
			}
		case "claude":
			sawClaude = true
			if ai.command {
				t.Error("claude should be marked as an agent, not a command")
			}
		case "codex":
			sawCodex = true
		}
	}
	if !sawShell || !sawClaude || !sawCodex {
		t.Errorf("missing items: shell=%v claude=%v codex=%v", sawShell, sawClaude, sawCodex)
	}
}

// TestPhaseModelHonorsFilters verifies that when the TUI's `phaseAgent`
// Enter handler advances to `phaseModel`, the picker list is narrowed
// by the active -T (tags) and -F (family) filters via
// cfg.EligibleModels. Without EligibleModels, the picker would show
// every model for the agent+default-tag regardless of the CLI filter
// flags — defeating the entire PR 3b "Filter inputs" promise.
//
// The test builds the config inline (rather than via a reusable
// helper) because this is the only phaseModel test that exercises
// filter-aware catalog narrowing; the other tests use the simpler
// testConfig() shape.
func TestPhaseModelHonorsFilters(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama"},
		},
		Models: []config.Model{
			// Two code models in the gemma4 family.
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
			{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
			// One design model in a different family — must be filtered out
			// by the -T code,design AND -F gemma4 combo below (design tag
			// AND gemma4 family → still includes only the gemma4 family
			// models, so design-only is excluded because it isn't gemma4).
			{ID: "ollama/llama3:design", ProviderID: "ollama", Family: "llama3", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}

	// Build a model in phaseAgent (where phaseAgent Enter fires).
	// The picker list is built from buildAgentList so Enter advances
	// to phaseModel via the same production code path.
	m := model{
		cfg:    cfg,
		phase:  phaseAgent,
		width:  80,
		height: 24,
	}
	m.agentList = list.New(buildAgentList(cfg, 78, 22), list.NewDefaultDelegate(), 78, 22)
	// Select the "claude" row so the Enter handler picks the right agent.
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && !ai.command && ai.name == "claude" {
			m.agentList.Select(i)
			break
		}
	}
	m.activeTags = "code,design"
	m.activeFamily = "gemma4"

	gotModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm, ok := gotModel.(model)
	if !ok {
		t.Fatalf("Update(Enter) returned %T, want model", gotModel)
	}
	if nm.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel after phaseAgent Enter", nm.phase)
	}

	// Every item in the picker must be in the gemma4 family; the
	// design-tag llama3 model must be filtered out.
	items := nm.models.Items()
	if len(items) == 0 {
		t.Fatal("picker list is empty after phaseAgent Enter")
	}
	for _, it := range items {
		mi, ok := it.(modelItem)
		if !ok {
			t.Fatalf("picker item is %T, want modelItem", it)
		}
		if mi.model.Family != "gemma4" {
			t.Errorf("model %s has family %q, want gemma4 (filter -F gemma4 not honored)",
				mi.model.ID, mi.model.Family)
		}
	}
}
