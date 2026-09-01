package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// stubInstalled overrides the installed test seam so agentIssue treats the
// given names as installed and everything else as not installed. Returns a
// cleanup that restores the real agents.Installed. Tests use it so the
// "installed" check is deterministic regardless of the host's binaries.
func stubInstalled(names ...string) func() {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	prev := installed
	installed = func(name string) bool { return set[name] }
	return func() { installed = prev }
}

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
	if len(items) < 3 { // 2 configured agents + at least the shell command
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
// purely alphabetically — agents and commands interleaved by name, with no
// grouping by kind. Without sorting, the picker would follow config order
// and the nondeterministic agents.Names() map iteration, so the same config
// could render a different menu on every launch. Commands (shell) sort in among the agents rather than being pinned to the
// bottom.
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
	// All registered drivers (agy, claude, codex, copilot, opencode, pi, shell) sorted purely by name.
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
	t.Cleanup(stubInstalled("claude"))
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
	cfg.ExposeAllForTest()

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
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.ExposeAllForTest()
	return cfg
}

// buildModelInPhaseAgent constructs a model in phaseAgent with the agent
// row pre-selected. Mirrors the setup in TestPhaseModelHonorsFilters so
// the production phaseAgent Enter handler fires against the same shape.
func buildModelInPhaseAgent(t *testing.T, cfg *config.Config) model {
	t.Helper()
	t.Cleanup(stubInstalled("claude"))
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
	requireBinary(t, "claude")
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
	requireBinary(t, "claude")
	t.Cleanup(stubInstalled("claude"))
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

// TestBuildAgentListShowsIssues verifies the picker rows carry a non-empty
// issue for agents that cannot launch (not configured, not installed) and an
// empty issue for launchable agents and commands. Without this, the picker
// would offer agents that silently fail on selection — the exact bug this
// change fixes.
func TestBuildAgentListShowsIssues(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "definitely-not-installed", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude"))

	issues := map[string]string{}
	for _, it := range buildAgentList(cfg) {
		ai := it.(agentItem)
		issues[ai.name] = ai.issue
	}
	if issues["claude"] != "" {
		t.Errorf("claude issue = %q, want \"\"", issues["claude"])
	}
	if issues["definitely-not-installed"] == "" {
		t.Error("definitely-not-installed should carry a not-installed issue")
	}
	if issues["opencode"] == "" {
		t.Error("opencode (registered but not configured) should carry a not-configured issue")
	}
	if issues["shell"] != "" {
		t.Errorf("shell issue = %q, want \"\" (command)", issues["shell"])
	}
}

// TestPhaseAgentEnterBlocksUnconfiguredAgent verifies that selecting an
// agent that is registered but not configured (e.g. opencode missing
// from config.toml) does not advance to the model screen; it stays on the
// picker and surfaces a clear "not configured" status instead of the old
// cryptic "agent not found" that looked like "nothing happens".
func TestPhaseAgentEnterBlocksUnconfiguredAgent(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude"))

	m := model{cfg: cfg, phase: phaseAgent, width: 80, height: 24}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && !ai.command && ai.name == "opencode" {
			m.agentList.Select(i)
			break
		}
	}

	gotModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := gotModel.(model)
	if nm.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent (selection blocked)", nm.phase)
	}
	if cmd != nil {
		t.Errorf("expected no cmd, got %v", cmd)
	}
	if !strings.Contains(nm.status, "not configured") {
		t.Errorf("status = %q, want to mention not configured", nm.status)
	}
}

// TestPhaseAgentEnterBlocksUninstalledAgent verifies that selecting a
// configured-but-uninstalled agent (e.g. codex/copilot with no binary on
// PATH) is blocked on the picker with a clear "not installed" status, rather
// than opening a model screen whose launch can never succeed.
func TestPhaseAgentEnterBlocksUninstalledAgent(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "definitely-not-installed", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude"))

	m := model{cfg: cfg, phase: phaseAgent, width: 80, height: 24}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && !ai.command && ai.name == "definitely-not-installed" {
			m.agentList.Select(i)
			break
		}
	}

	gotModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := gotModel.(model)
	if nm.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent (selection blocked)", nm.phase)
	}
	if cmd != nil {
		t.Errorf("expected no cmd, got %v", cmd)
	}
	if !strings.Contains(nm.status, "not installed") {
		t.Errorf("status = %q, want to mention not installed", nm.status)
	}
}

// TestBuildAgentListAdapter verifies buildAgentList is a thin wrapper around
// agents.ListEntries, preserving command classification and issue text.
func TestBuildAgentListAdapter(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude"))

	items := buildAgentList(cfg)
	if len(items) == 0 {
		t.Fatal("expected items")
	}

	// Every item must be an agentItem derived from an AgentListEntry.
	for _, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			t.Fatalf("item %T is not an agentItem", it)
		}
		// Commands have no issue; non-commands may have an issue.
		if ai.command && ai.issue != "" {
			t.Errorf("command %q has issue %q", ai.name, ai.issue)
		}
	}
}
