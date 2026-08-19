package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
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
	items := buildAgentList(cfg)
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

// TestBuildAgentListOmitsUnconfiguredAgents asserts that registered driver
// names absent from cfg.Agents (and not commands) never appear in the
// picker, and that the resulting rows are deterministic across runs.
// Before the fix, buildAgentList appended every registered driver, so a
// config declaring only claude still showed codex/copilot/opencode/pi rows
// that errored "agent not found" on Enter — and in nondeterministic order,
// since agents.Names() ranges a map.
func TestBuildAgentListOmitsUnconfiguredAgents(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	items := buildAgentList(cfg)
	var sawShell bool
	for _, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			t.Fatalf("item %T is not an agentItem", it)
		}
		if ai.name == "shell" {
			sawShell = true
			if !ai.command {
				t.Error("shell should be marked as a command")
			}
			continue
		}
		if !ai.command && ai.name != "claude" {
			t.Errorf("unconfigured agent %q appears in picker (dead-end row)", ai.name)
		}
	}
	if !sawShell {
		t.Error("missing shell command row (registered commands must still appear)")
	}

	// Determinism: a second build must produce the identical row order.
	second := buildAgentList(cfg)
	if len(items) != len(second) {
		t.Fatalf("item count differs between builds: %d vs %d", len(items), len(second))
	}
	for i := range items {
		if items[i].(agentItem).name != second[i].(agentItem).name {
			t.Errorf("order differs at %d: %q vs %q", i, items[i].(agentItem).name, second[i].(agentItem).name)
		}
	}
}

// TestPhaseAgentViewRendersStatus asserts that an error stored in m.status
// on the phaseAgent screen is drawn by the View. Before the fix,
// phaseAgentView rendered only header + list + footer, so every error path
// in the agent+command picker (config error, empty model catalog, launch
// failure) was silent — the user saw an unchanged picker with no feedback.
func TestPhaseAgentViewRendersStatus(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := model{phase: phaseAgent, cfg: cfg, width: 80, height: 24}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	const want = `no models for agent "claude" in tag "code"`
	m.status = want + " — edit your config"
	view := m.phaseAgentView()
	if !strings.Contains(view, want) {
		t.Errorf("phaseAgentView missing status %q:\n%s", want, view)
	}
}
