package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
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

// TestBuildAgentListOrdering asserts the agent+command picker is ordered
// deterministically: agents alphabetically, then commands alphabetically.
// Without sorting, the picker would follow config order and the
// nondeterministic agents.Names() map iteration, so the same config could
// render a different menu on every launch. shell (the only command today)
// must always be last.
func TestBuildAgentListOrdering(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "codex"}, {Name: "copilot"}, {Name: "claude"}, {Name: "pi"},
		},
	}
	items := buildAgentList(cfg)
	names := make([]string, 0, len(items))
	for _, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			t.Fatalf("item %T is not an agentItem", it)
		}
		names = append(names, ai.name)
	}
	// All registered agents (agy, claude, codex, copilot, opencode, pi) plus
	// the shell command, sorted: agents alphabetically, then commands.
	want := []string{"agy", "claude", "codex", "copilot", "opencode", "pi", "shell"}
	if len(names) != len(want) {
		t.Fatalf("got %d items %v, want %d: %v", len(names), names, len(want), want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("item[%d] = %q, want %q (full: %v)", i, names[i], w, names)
		}
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
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
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

// singleModelConfig returns a config where the claude agent has exactly
// one eligible code model. Used by the single-model picker-skip tests
// below; mirrors the catalog-narrowing shape of TestPhaseModelHonorsFilters
// but with one item, so EligibleModels returns a 1-element slice.
func singleModelConfig() *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
}

// buildModelInPhaseAgent constructs a model in phaseAgent with the agent
// row pre-selected. Mirrors the setup in TestPhaseModelHonorsFilters so
// the production phaseAgent Enter handler fires against the same shape.
func buildModelInPhaseAgent(t *testing.T, cfg *config.Config) model {
	t.Helper()
	m := model{
		cfg:    cfg,
		phase:  phaseAgent,
		width:  80,
		height: 24,
	}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && !ai.command && ai.name == "claude" {
			m.agentList.Select(i)
			break
		}
	}
	return m
}

// TestPhaseAgentEnterSkipsPickerWhenSingleModel asserts that when the
// eligible model list contains exactly one item, Enter in phaseAgent
// bypasses the model picker and runs the agent immediately. Regression
// coverage for the spec'd single-model picker-skip behavior — without
// this branch, a user with one eligible model still sees a 1-item picker
// and has to press Enter twice (once to confirm, once to launch).
func TestPhaseAgentEnterSkipsPickerWhenSingleModel(t *testing.T) {
	m := buildModelInPhaseAgent(t, singleModelConfig())
	m.selectedPath = t.TempDir()

	gotModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := gotModel.(model)
	if nm.phase == phaseModel {
		t.Fatalf("phase = phaseModel; want picker-skip (phaseResume or launch cmd)")
	}
	if cmd == nil {
		t.Fatal("expected launch cmd from single-model skip; got nil")
	}
}

// TestPhaseAgentEnterSingleModelShowsResumePrompt asserts that when the
// eligible list is one model AND a prior claude session exists, the skip
// path goes to phaseResume (the resume prompt still applies — skipping
// the picker doesn't bypass the user's choice between resume/fresh).
func TestPhaseAgentEnterSingleModelShowsResumePrompt(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	repo := t.TempDir()
	slug := session.Slug(repo)
	sessDir := filepath.Join(homeDir, ".claude", "projects", slug)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "session.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	m := buildModelInPhaseAgent(t, singleModelConfig())
	m.selectedPath = repo

	gotModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := gotModel.(model)
	if nm.phase != phaseResume {
		t.Fatalf("phase = %v, want phaseResume (skip must still run session check)", nm.phase)
	}
	if cmd != nil {
		t.Errorf("expected no cmd while showing resume prompt, got %v", cmd)
	}
}

// TestPinnedAgentSingleModelSkipsPicker asserts the CLI-pinned path
// (`wt -A claude` with one eligible model) also skips the picker. This
// is the selectedEntryMsg handler with m.initialAgent set; the same
// enterModelPhase helper drives both code paths, so a regression here
// would surface in either.
func TestPinnedAgentSingleModelSkipsPicker(t *testing.T) {
	m := model{
		cfg:          singleModelConfig(),
		phase:        phaseList,
		width:        80,
		height:       24,
		initialAgent: "claude",
		activeTags:   "code",
	}
	m.selectedPath = t.TempDir()

	gotModel, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	nm := gotModel.(model)
	if nm.phase == phaseModel {
		t.Fatalf("phase = phaseModel; want picker-skip for pinned-agent CLI path with single model")
	}
	if cmd == nil {
		t.Fatal("expected launch cmd from single-model skip in pinned-agent path; got nil")
	}
}
