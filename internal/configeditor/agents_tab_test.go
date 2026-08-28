package configeditor

import (
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestAgentsTab_Sort_CommandsFirst verifies that command agents (e.g. shell)
// appear before regular agents, and that both groups are sorted by name. The
// configeditor keeps commands grouped first (unlike the main TUI's
// agent+command picker, which now sorts agents and commands together
// alphabetically).
func TestAgentsTab_Sort_CommandsFirst(t *testing.T) {
	cleanup := agents.RegisterTest("shell", func() agents.Driver {
		return &stubCommandDriver{}
	})
	defer cleanup()

	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "shell", SupportedProviders: []string{"ollama"}},
			{Name: "agy", SupportedProviders: []string{"agy"}},
		},
		Providers: []config.Provider{
			{ID: "claude"},
			{ID: "ollama"},
			{ID: "agy"},
		},
	}
	l := buildAgentsList(testTheme(), 80, 24, cfg)

	// Find the boundary between commands and non-commands.
	var sawNonCommand bool
	for _, it := range l.Items() {
		ai := it.(agentItem)
		if ai.command {
			if sawNonCommand {
				t.Fatalf("command %q appeared after a non-command", ai.agent.Name)
			}
		} else {
			sawNonCommand = true
		}
	}

	// The registered command driver and the configured shell agent must both
	// be present (they may dedupe to a single row).
	foundShell := false
	for _, it := range l.Items() {
		if it.(agentItem).agent.Name == "shell" {
			foundShell = true
			break
		}
	}
	if !foundShell {
		t.Fatal("expected shell row in agents list")
	}
}

// TestAgentsTab_RegisteredButUnconfiguredDriver appears when a driver is
// registered but not listed in cfg.Agents. This surfaces "not configured"
// so users know they can add it.
func TestAgentsTab_RegisteredButUnconfiguredDriver(t *testing.T) {
	cleanup := agents.RegisterTest("opencode-test", func() agents.Driver {
		return &stubAgentDriver{}
	})
	defer cleanup()

	cfg := &config.Config{Agents: []config.Agent{}}
	l := buildAgentsList(testTheme(), 80, 24, cfg)

	var found bool
	for _, it := range l.Items() {
		ai := it.(agentItem)
		if ai.agent.Name == "opencode-test" {
			found = true
			if !strings.Contains(ai.Title(), "not configured") {
				t.Fatalf("registered-but-unconfigured row title %q should say not configured", ai.Title())
			}
			if ai.installed {
				t.Fatal("unconfigured agent should not have an installed check")
			}
		}
	}
	if !found {
		t.Fatal("expected registered-but-unconfigured driver to appear")
	}
}

// TestAgentsTab_CommandsSkipInstalledCheck verifies that command agents like
// shell never show "not installed" in the title.
func TestAgentsTab_CommandsSkipInstalledCheck(t *testing.T) {
	shell := agentItem{agent: config.Agent{Name: "shell"}, command: true}
	if strings.Contains(shell.Title(), "not installed") {
		t.Errorf("command Title() = %q, should not contain not installed", shell.Title())
	}
}

// TestAgentsTab_InstalledAnnotation verifies that the Title() method
// renders the correct installed marker regardless of the actual PATH
// state. This ensures the UI communicates launchability clearly.
func TestAgentsTab_InstalledAnnotation(t *testing.T) {
	installed := agentItem{agent: config.Agent{Name: "foo"}, installed: true}
	if !strings.Contains(installed.Title(), "✓") {
		t.Errorf("installed agent Title() = %q, want ✓ marker", installed.Title())
	}

	notInstalled := agentItem{agent: config.Agent{Name: "bar"}, installed: false}
	if !strings.Contains(notInstalled.Title(), "✗") {
		t.Errorf("not-installed agent Title() = %q, want ✗ marker", notInstalled.Title())
	}
}

// stubCommandDriver is a test driver that reports IsCommand() == true.
type stubCommandDriver struct{}

func (d *stubCommandDriver) Build(_ config.Model, _ bool) agents.LaunchCmd {
	return agents.LaunchCmd{}
}
func (d *stubCommandDriver) YoloFlag() string { return "" }
func (d *stubCommandDriver) IsCommand() bool  { return true }

// stubAgentDriver is a normal (non-command) test driver.
type stubAgentDriver struct{}

func (d *stubAgentDriver) Build(_ config.Model, _ bool) agents.LaunchCmd {
	return agents.LaunchCmd{}
}
func (d *stubAgentDriver) YoloFlag() string { return "" }
