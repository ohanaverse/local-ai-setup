package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestModelsCmd_PrintsMergedRegistry verifies that `wt models` renders the
// merged registry (curated + discovered) as a table. We stub the discovery
// function so the test does not shell out to ollama or hit the network.
func TestModelsCmd_PrintsMergedRegistry(t *testing.T) {
	old := registryDiscover
	registryDiscover = func(cfg *config.Config) []config.Model {
		return []config.Model{
			{ID: "curated/model", ProviderID: "curated", Source: config.SourceCurated},
			{ID: "discovered/model", ProviderID: "discovered", Source: config.SourceDiscovered},
		}
	}
	defer func() { registryDiscover = old }()

	a, _ := newTestApp(t)
	cmd := modelsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	outStr := out.String()
	if !strings.Contains(outStr, "curated/model") {
		t.Errorf("output should contain curated model; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "discovered/model") {
		t.Errorf("output should contain discovered model; got:\n%s", outStr)
	}
}

// TestAgentsCmd_PrintsConfiguredAndRegistered verifies that `wt agents`
// lists configured agents and registered drivers, marking commands
// separately from model agents.
func TestAgentsCmd_PrintsConfiguredAndRegistered(t *testing.T) {
	cleanup := agents.RegisterTest("shell", func() agents.Driver {
		return &stubCommandDriver{}
	})
	defer cleanup()

	a, _ := newTestApp(t)
	a.cfg.Agents = []config.Agent{
		{Name: "claude", SupportedProviders: []string{"claude"}},
		{Name: "shell", SupportedProviders: []string{"ollama"}},
	}
	a.cfg.Providers = []config.Provider{
		{ID: "claude"},
		{ID: "ollama"},
	}
	cmd := agentsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	outStr := out.String()
	if !strings.Contains(outStr, "claude") {
		t.Errorf("output should contain claude; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "shell") {
		t.Errorf("output should contain shell; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "command") {
		t.Errorf("output should mark shell as a command; got:\n%s", outStr)
	}
}

// stubCommandDriver is a test driver that reports IsCommand() == true.
type stubCommandDriver struct{}

func (d *stubCommandDriver) Build(_ config.Model, _ bool) agents.LaunchCmd {
	return agents.LaunchCmd{}
}
func (d *stubCommandDriver) YoloFlag() string { return "" }
func (d *stubCommandDriver) IsCommand() bool { return true }
